package adapter

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	librsync "github.com/balena-os/librsync-go"
)

// sigDigester is satisfied by *librsync.signature, which is unexported.
// Interface satisfaction is structural in Go, so this works across packages
// as long as the methods used are exported.
type sigDigester interface {
	Digest(b []byte) error
	End() *librsync.SignatureType
}

// ── Batch ─────────────────────────────────────────────────────────────────────

// SignatureBytes generates a serialized signature from a complete file buffer.
func SignatureBytes(input []byte, blockLen, strongLen uint32, sigType librsync.MagicNumber) ([]byte, error) {
	var out bytes.Buffer
	numBlocks := (len(input) + int(blockLen) - 1) / int(blockLen)
	out.Grow(12 + numBlocks*(4+int(strongLen)))
	if _, err := librsync.Signature(bytes.NewReader(input), &out, blockLen, strongLen, sigType); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// ParseSignature parses serialized signature bytes into an in-memory structure.
// The input bytes are fully consumed and copied; the caller may free them after
// this call returns.
func ParseSignature(sigBytes []byte) (*librsync.SignatureType, error) {
	return librsync.ReadSignature(bytes.NewReader(sigBytes))
}

// DeltaBytes computes a delta between a new file and a parsed signature.
func DeltaBytes(sig *librsync.SignatureType, input []byte) ([]byte, error) {
	var out bytes.Buffer
	litBuf := make([]byte, 0, librsync.OUTPUT_BUFFER_SIZE)
	if err := librsync.DeltaBuff(sig, bytes.NewReader(input), &out, litBuf); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// PatchBytes applies a delta to a complete base file buffer.
func PatchBytes(base, deltaBytes []byte) ([]byte, error) {
	var out bytes.Buffer
	out.Grow(len(base))
	if err := librsync.Patch(bytes.NewReader(base), bytes.NewReader(deltaBytes), &out); err != nil {
		return nil, err
	}
	return normalizeEmpty(out.Bytes()), nil
}

// ── Callback-based patch ──────────────────────────────────────────────────────

// ReadAtFunc reads up to len(buf) bytes from the source at the given absolute
// offset. Partial reads are only permitted at EOF; otherwise the buffer must be
// filled in full or an error returned.
type ReadAtFunc func(offset int64, buf []byte) (int, error)

// callbackReadSeeker wraps a ReadAtFunc as an io.ReadSeeker. It tracks position
// internally; Seek uses SeekStart only (which is all Patch ever uses).
type callbackReadSeeker struct {
	readAt ReadAtFunc
	pos    int64
}

func (c *callbackReadSeeker) Read(p []byte) (int, error) {
	n, err := c.readAt(c.pos, p)
	c.pos += int64(n)
	return n, err
}

func (c *callbackReadSeeker) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		c.pos = offset
	case io.SeekCurrent:
		c.pos += offset
	case io.SeekEnd:
		return 0, fmt.Errorf("librsync: SeekEnd not supported by callback reader")
	}
	return c.pos, nil
}

// PatchWithCallback applies a delta to a base file accessed through a ReadAtFunc.
// The base file does not need to be fully loaded into memory.
func PatchWithCallback(readAt ReadAtFunc, deltaBytes []byte) ([]byte, error) {
	var out bytes.Buffer
	base := &callbackReadSeeker{readAt: readAt}
	if err := librsync.Patch(base, bytes.NewReader(deltaBytes), &out); err != nil {
		return nil, err
	}
	return normalizeEmpty(out.Bytes()), nil
}

// ── Streaming Signature ───────────────────────────────────────────────────────

// SignatureSession streams serialized signature bytes as input is fed in
// arbitrary-sized chunks.
//
// Unlike the delta algorithm, the core signature implementation is not stateful
// across Digest calls — each call is independent. To handle arbitrary chunk
// sizes correctly, SignatureSession buffers bytes internally until a complete
// block accumulates before passing data to the core library.
//
// Consequences for callers:
//
//   - The first Feed call always returns the 12-byte signature header,
//     regardless of whether any complete blocks were processed.
//   - Subsequent Feed calls may return nil if accumulated bytes have not yet
//     reached a block boundary. Output appears in bursts once enough data for
//     one or more complete blocks has accumulated.
//   - With chunk sizes smaller than block_len, many consecutive feeds will
//     return nil.
//
// End flushes the final partial block and must be called exactly once. After
// End returns, the session must not be used again.
type SignatureSession struct {
	sig      sigDigester
	out      *bytes.Buffer
	pending  []byte
	blockLen uint32
	ended    bool // set by first EndInto call so the final flush only runs once
	err      error
}

// errSigEnded / errDeltaEnded are sticky sentinels stored in .err once EndInto
// has fully drained, so subsequent EndInto calls return (0, nil) instead of
// re-running the (already idempotent) End logic.
var (
	errSigEnded   = errors.New("librsync: signature session ended")
	errDeltaEnded = errors.New("librsync: delta session ended")
)

func NewSignatureSession(blockLen, strongLen uint32, sigType librsync.MagicNumber) (*SignatureSession, error) {
	out := &bytes.Buffer{}
	sig, err := librsync.NewSignature(sigType, blockLen, strongLen, out)
	if err != nil {
		return nil, err
	}
	return &SignatureSession{sig: sig, out: out, blockLen: blockLen}, nil
}

// Feed processes a chunk of input. Returns any output produced, which may be
// nil if no complete blocks have accumulated yet. Once Feed returns an error,
// the session is valid only for abandonment — do not call Feed again.
func (s *SignatureSession) Feed(input []byte) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	s.pending = append(s.pending, input...)
	complete := (len(s.pending) / int(s.blockLen)) * int(s.blockLen)
	if complete > 0 {
		if err := s.sig.Digest(s.pending[:complete]); err != nil {
			s.err = err
			return nil, err
		}
		s.pending = append(s.pending[:0], s.pending[complete:]...)
	}
	// Always drain so the header (written during NewSignature) is returned
	// on the first call regardless of whether any blocks were processed.
	return drainBuf(s.out), nil
}

// End flushes the final partial block and invalidates the session.
func (s *SignatureSession) End() ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	if len(s.pending) > 0 {
		if err := s.sig.Digest(s.pending); err != nil {
			s.err = err
			return nil, err
		}
	}
	s.sig.End()
	return drainBuf(s.out), nil
}

// FeedInto is the zero-allocation variant of Feed: it digests input (which may
// be empty as a pure drain request) and writes up to len(dst) bytes of output
// directly into dst, returning the number of bytes written and a morePending
// flag.
//
// morePending is true when output remains buffered internally — the caller
// should drain by calling FeedInto again with empty input until it returns
// morePending=false.
func (s *SignatureSession) FeedInto(input, dst []byte) (n int, morePending bool, err error) {
	if s.err != nil {
		return 0, false, s.err
	}
	if len(input) > 0 {
		s.pending = append(s.pending, input...)
		complete := (len(s.pending) / int(s.blockLen)) * int(s.blockLen)
		if complete > 0 {
			if err := s.sig.Digest(s.pending[:complete]); err != nil {
				s.err = err
				return 0, false, err
			}
			s.pending = append(s.pending[:0], s.pending[complete:]...)
		}
	}
	n = drainInto(s.out, dst)
	return n, s.out.Len() > 0, nil
}

// EndInto is the zero-allocation variant of End. On the first call it flushes
// the final partial block; subsequent calls drain remaining output in
// dst-sized chunks. Returns the number of bytes written and a morePending
// flag — drain by calling repeatedly until morePending=false.
func (s *SignatureSession) EndInto(dst []byte) (n int, morePending bool, err error) {
	if s.err != nil {
		if s.err == errSigEnded {
			return 0, false, nil
		}
		return 0, false, s.err
	}
	if !s.ended {
		if len(s.pending) > 0 {
			if err := s.sig.Digest(s.pending); err != nil {
				s.err = err
				return 0, false, err
			}
			s.pending = s.pending[:0]
		}
		s.sig.End()
		s.ended = true
	}
	n = drainInto(s.out, dst)
	morePending = s.out.Len() > 0
	if !morePending {
		s.err = errSigEnded
	}
	return n, morePending, nil
}

// ── Streaming Delta ───────────────────────────────────────────────────────────

// DeltaSession streams delta bytes as input is fed in arbitrary-sized chunks.
// The DeltaStruct it wraps is stateful between Feed calls, so arbitrary chunk
// sizes are handled correctly — block boundaries are tracked internally.
//
// Feed may return nil if the internal literal buffer (up to 16 MB) has not yet
// flushed. End is guaranteed to flush everything remaining.
type DeltaSession struct {
	delta *librsync.DeltaStruct
	out   *bytes.Buffer
	ended bool // set by first EndInto call so delta.End() only runs once
	err   error
}

func NewDeltaSession(sig *librsync.SignatureType) (*DeltaSession, error) {
	out := &bytes.Buffer{}
	out.Grow(int(librsync.OUTPUT_BUFFER_SIZE))
	delta, err := librsync.NewDelta(sig, out, int(librsync.OUTPUT_BUFFER_SIZE))
	if err != nil {
		return nil, err
	}
	return &DeltaSession{delta: delta, out: out}, nil
}

// Feed processes a chunk of the new file. Returns any delta output produced.
// Once Feed returns an error, the session is valid only for abandonment.
func (s *DeltaSession) Feed(input []byte) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	if err := s.delta.Digest(input); err != nil {
		s.err = err
		return nil, err
	}
	return drainBuf(s.out), nil
}

// End finalizes the delta and invalidates the session.
func (s *DeltaSession) End() ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	if err := s.delta.End(); err != nil {
		s.err = err
		return nil, err
	}
	return drainBuf(s.out), nil
}

// FeedInto is the zero-allocation variant of Feed: it digests input (which may
// be empty as a pure drain request) and writes up to len(dst) bytes of output
// directly into dst, returning the number of bytes written and a morePending
// flag (true if output remains buffered internally; drain with empty input).
func (s *DeltaSession) FeedInto(input, dst []byte) (n int, morePending bool, err error) {
	if s.err != nil {
		return 0, false, s.err
	}
	if len(input) > 0 {
		if err := s.delta.Digest(input); err != nil {
			s.err = err
			return 0, false, err
		}
	}
	n = drainInto(s.out, dst)
	return n, s.out.Len() > 0, nil
}

// EndInto is the zero-allocation variant of End. Drains remaining output in
// dst-sized chunks. Returns morePending=true while more output is buffered;
// call repeatedly until morePending=false.
func (s *DeltaSession) EndInto(dst []byte) (n int, morePending bool, err error) {
	if s.err != nil {
		if s.err == errDeltaEnded {
			return 0, false, nil
		}
		return 0, false, s.err
	}
	if !s.ended {
		if err := s.delta.End(); err != nil {
			s.err = err
			return 0, false, err
		}
		s.ended = true
	}
	n = drainInto(s.out, dst)
	morePending = s.out.Len() > 0
	if !morePending {
		s.err = errDeltaEnded
	}
	return n, morePending, nil
}

// ── Streaming Patch ───────────────────────────────────────────────────────────

// errPatchEnded is the sticky sentinel stored in PatchSession.err after a
// successful End so that repeated End calls return (nil, nil) rather than
// re-running or blocking.
var errPatchEnded = errors.New("librsync: patch session ended")

// chanWriter relays Patch output chunks to the caller via a buffered channel.
// Each Write call copies its bytes so the Patch goroutine's stack is not held.
// The quit channel is closed by Close() to unblock a stuck Write when the
// caller abandons the session without calling End.
type chanWriter struct {
	ch   chan []byte
	quit <-chan struct{}
}

func (w *chanWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	chunk := make([]byte, len(p))
	copy(chunk, p)
	select {
	case w.ch <- chunk:
		return len(p), nil
	case <-w.quit:
		return 0, errors.New("librsync: patch session closed")
	}
}

// PatchSession streams a patch operation with bounded memory usage.
//
// Delta bytes are fed in arbitrary chunks via Feed; each call returns whatever
// output the Patch goroutine has produced so far (may be nil). End signals
// end-of-delta and returns all remaining output.
//
// Internally, librsync.Patch runs in a goroutine connected to the caller by
// two io.Pipe channels so that neither the delta stream nor the output is ever
// fully buffered in memory:
//
//   - Delta input:  io.Pipe — zero internal buffer; bytes pass straight to Patch.
//   - Output:       buffered channel of ≤32 KB chunks — at most ~1 MB in flight.
//   - Base file:    accessed via ReadAtFunc — never loaded into memory.
//
// The ReadAtFunc and any resources it references must remain valid until End or
// Close returns.
type PatchSession struct {
	deltaW   *io.PipeWriter
	outCh    chan []byte
	errCh    chan error
	quit     chan struct{}
	leftover []byte // chunk slice from outCh that didn't fit in the last dst
	ended    bool   // set by first EndInto call so deltaW.Close() only runs once
	err      error  // sticky: set on first error or successful End
}

func NewPatchSession(readAt ReadAtFunc) *PatchSession {
	deltaR, deltaW := io.Pipe()
	outCh := make(chan []byte, 32) // ~1 MB max in-flight at 32 KB/chunk
	errCh := make(chan error, 1)
	quit := make(chan struct{})

	go func() {
		base := &callbackReadSeeker{readAt: readAt}
		err := librsync.Patch(base, deltaR, &chanWriter{ch: outCh, quit: quit})
		// CloseWithError wakes any blocked deltaW.Write and returns that error
		// to the writer goroutine spawned in Feed, preventing a goroutine leak.
		if err != nil {
			deltaR.CloseWithError(err)
		} else {
			deltaR.Close()
		}
		close(outCh)
		errCh <- err
	}()

	return &PatchSession{
		deltaW: deltaW,
		outCh:  outCh,
		errCh:  errCh,
		quit:   quit,
	}
}

// Feed sends a delta chunk to the Patch goroutine and returns whatever output
// has been produced so far. The returned slice may be nil if no output is
// ready yet — all output is guaranteed to be flushed by End.
//
// Feed writes the delta to the pipe in a goroutine and simultaneously drains
// available output. This prevents deadlock: if the output channel is full,
// draining it unblocks the Patch goroutine so it can consume more delta.
//
// Once Feed returns an error the session is valid only for Close.
func (s *PatchSession) Feed(delta []byte) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	if len(delta) == 0 {
		return nil, nil
	}

	writeErr := make(chan error, 1)
	go func() {
		_, err := s.deltaW.Write(delta)
		writeErr <- err
	}()

	var out []byte
	for {
		select {
		case err := <-writeErr:
			if err != nil {
				s.err = err
				return out, err
			}
			// Write consumed; non-blocking drain of any queued output.
			for {
				select {
				case chunk, ok := <-s.outCh:
					if !ok {
						// Patch already returned; error (if any) surfaces in End.
						return out, nil
					}
					out = append(out, chunk...)
				default:
					return out, nil
				}
			}
		case chunk, ok := <-s.outCh:
			if !ok {
				// outCh closed: Patch returned before Write finished.
				// Nil the field so future select iterations don't spin on it;
				// writeErr will arrive momentarily.
				s.outCh = nil
			} else {
				out = append(out, chunk...)
			}
		}
	}
}

// End signals end-of-delta and returns all remaining output.
// Always invalidates the session — do not call Feed or End again after this.
// Calling End on an already-ended session is a no-op that returns (nil, nil).
func (s *PatchSession) End() ([]byte, error) {
	if s.err != nil {
		if s.err == errPatchEnded {
			return nil, nil
		}
		return nil, s.err
	}
	s.deltaW.Close() // signal EOF to Patch goroutine
	var out []byte
	for s.outCh != nil {
		chunk, ok := <-s.outCh
		if !ok {
			break
		}
		out = append(out, chunk...)
	}
	err := <-s.errCh
	if err != nil {
		s.err = err
	} else {
		s.err = errPatchEnded
	}
	return normalizeEmpty(out), err
}

// FeedInto is the zero-allocation variant of Feed: it sends delta to the Patch
// goroutine (delta may be empty as a pure drain request) and writes up to
// len(dst) bytes of reconstructed output directly into dst, returning the
// number of bytes written and a morePending flag.
//
// morePending is true when output is buffered internally (in leftover) — the
// caller should drain by calling FeedInto again with empty input until it
// returns morePending=false. Note: morePending only reports buffered output,
// not output still pending in the channel; another Feed/End call may surface
// more output even after morePending=false.
func (s *PatchSession) FeedInto(delta, dst []byte) (n int, morePending bool, err error) {
	if s.err != nil {
		return 0, false, s.err
	}
	if len(dst) == 0 {
		return 0, len(s.leftover) > 0, nil
	}

	// Fast path: serve from leftover if any, without touching the channel.
	written := s.consumeLeftover(dst)
	if written == len(dst) {
		// dst is full; if delta is nil this is a pure drain request, otherwise
		// we still need to send the delta. The caller will see a "full dst"
		// return and call again with empty input — but if they passed delta
		// here, dropping it would be wrong. Send it via the same goroutine
		// dance Feed uses; sendDelta stashes any new output in s.leftover.
		if len(delta) > 0 {
			if _, err := s.sendDelta(delta, dst[:0]); err != nil {
				return written, len(s.leftover) > 0, err
			}
		}
		return written, len(s.leftover) > 0, nil
	}

	if len(delta) == 0 {
		// Pure drain request: try to pull more from the channel without
		// blocking on the producer (the producer hasn't been pushed by new
		// input, but it may have buffered chunks ready).
		written += s.drainChannelInto(dst[written:])
		return written, len(s.leftover) > 0, nil
	}

	m, err := s.sendDelta(delta, dst[written:])
	written += m
	if err != nil {
		return written, len(s.leftover) > 0, err
	}
	return written, len(s.leftover) > 0, nil
}

// EndInto is the zero-allocation variant of End. On the first call it signals
// EOF to the Patch goroutine; subsequent calls drain any remaining output in
// dst-sized chunks. Returns morePending=true while output remains; call
// repeatedly until morePending=false.
func (s *PatchSession) EndInto(dst []byte) (n int, morePending bool, err error) {
	if s.err != nil {
		if s.err == errPatchEnded {
			return 0, false, nil
		}
		return 0, false, s.err
	}
	if !s.ended {
		s.deltaW.Close() // signal EOF; goroutine will close outCh after finishing.
		s.ended = true
	}

	written := s.consumeLeftover(dst)

	// Pull from outCh until dst is full or channel closes. Guard against
	// receiving from a nil channel — once outCh closes we set it to nil and
	// further receives would block forever.
	for written < len(dst) && s.outCh != nil {
		chunk, ok := <-s.outCh
		if !ok {
			s.outCh = nil
			break
		}
		written += s.spillIntoDst(chunk, dst[written:])
	}

	// Once both the channel is closed and no leftover remains, the goroutine
	// has fully exited — drain errCh exactly once and mark the session done.
	if s.outCh == nil && len(s.leftover) == 0 {
		err := <-s.errCh
		if err != nil {
			s.err = err
			return written, false, err
		}
		s.err = errPatchEnded
		return written, false, nil
	}
	return written, true, nil
}

// consumeLeftover writes as many leftover bytes as fit into dst and returns
// the count. Mutates s.leftover to hold any remainder.
func (s *PatchSession) consumeLeftover(dst []byte) int {
	if len(s.leftover) == 0 || len(dst) == 0 {
		return 0
	}
	n := copy(dst, s.leftover)
	if n == len(s.leftover) {
		s.leftover = nil
	} else {
		s.leftover = s.leftover[n:]
	}
	return n
}

// spillIntoDst copies as much of chunk as fits into dst and appends the rest
// to s.leftover. Returns bytes written to dst.
//
// Append (not assign) matters because sendDelta may receive several chunks in
// a row when the producer is faster than the consumer's dst can absorb; each
// overflow must be queued, not overwrite the previous one.
func (s *PatchSession) spillIntoDst(chunk, dst []byte) int {
	n := copy(dst, chunk)
	if n < len(chunk) {
		s.leftover = append(s.leftover, chunk[n:]...)
	}
	return n
}

// drainChannelInto pulls available chunks from outCh non-blockingly and copies
// them into dst, spilling overflow into s.leftover. Returns bytes written.
func (s *PatchSession) drainChannelInto(dst []byte) int {
	written := 0
	for written < len(dst) {
		select {
		case chunk, ok := <-s.outCh:
			if !ok {
				s.outCh = nil
				return written
			}
			written += s.spillIntoDst(chunk, dst[written:])
			if len(s.leftover) > 0 {
				return written
			}
		default:
			return written
		}
	}
	return written
}

// sendDelta writes delta to the pipe in a goroutine and concurrently drains
// the output channel into dst (preventing deadlock when outCh is full). Any
// produced output that does not fit in dst is stashed in s.leftover. Returns
// the number of bytes written to dst.
func (s *PatchSession) sendDelta(delta, dst []byte) (int, error) {
	writeErr := make(chan error, 1)
	go func() {
		_, err := s.deltaW.Write(delta)
		writeErr <- err
	}()

	written := 0
	for {
		select {
		case err := <-writeErr:
			if err != nil {
				s.err = err
				return written, err
			}
			// Write consumed; non-blocking drain to capture what's already ready.
			written += s.drainChannelInto(dst[written:])
			return written, nil
		case chunk, ok := <-s.outCh:
			if !ok {
				s.outCh = nil
			} else {
				written += s.spillIntoDst(chunk, dst[written:])
			}
		}
	}
}

// Close abandons the session without finalizing. Use on the error path when
// End has not been called. After Close, the session must not be used again.
func (s *PatchSession) Close() {
	if s.err != nil {
		return // goroutine already exited (error path) or End was called
	}
	s.err = errPatchEnded
	close(s.quit)                                             // unblock chanWriter if stuck on full outCh
	s.deltaW.CloseWithError(errors.New("librsync: abandoned")) // unblock Patch goroutine if stuck on Read
	for range s.outCh {                                        // drain so chanWriter can exit
	}
	<-s.errCh // wait for goroutine to fully exit
}

// ── Internal helpers ──────────────────────────────────────────────────────────

// normalizeEmpty converts a nil byte slice to a non-nil empty slice. Patch
// operations on empty files return nil from bytes.Buffer.Bytes(); callers
// that compare with os.ReadFile output (which returns []byte{}) need consistency.
func normalizeEmpty(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

// drainInto copies up to len(dst) bytes from buf into dst, removing the copied
// bytes from buf. Returns the number of bytes written. Any unread bytes remain
// in buf to be drained on a subsequent call.
//
// Note: bytes.Buffer.Read removes consumed bytes from the buffer (it tracks an
// internal read offset and resets when fully drained), so this is genuinely
// destructive — no separate Reset is needed.
func drainInto(buf *bytes.Buffer, dst []byte) int {
	if buf.Len() == 0 || len(dst) == 0 {
		return 0
	}
	n, _ := buf.Read(dst) // bytes.Buffer.Read never returns an error besides io.EOF (handled above)
	return n
}

// drainBuf returns a copy of buf's contents and resets it.
// Returns nil (not an empty slice) when there is nothing to drain.
func drainBuf(buf *bytes.Buffer) []byte {
	if buf.Len() == 0 {
		return nil
	}
	out := make([]byte, buf.Len())
	copy(out, buf.Bytes())
	buf.Reset()
	return out
}
