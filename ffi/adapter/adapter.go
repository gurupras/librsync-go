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
	err      error
}

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
	deltaW *io.PipeWriter
	outCh  chan []byte
	errCh  chan error
	quit   chan struct{}
	err    error // sticky: set on first error or successful End
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
