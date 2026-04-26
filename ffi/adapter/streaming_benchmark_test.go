package adapter_test

import (
	"bytes"
	"fmt"
	"math/rand"
	"testing"

	librsync "github.com/balena-os/librsync-go"
	"github.com/balena-os/librsync-go/ffi/adapter"
)

// Streaming throughput benchmarks for DeltaSession and PatchSession.
//
// Goal: report MB/s for the streaming Feed/End APIs at realistic sizes and
// chunk granularities. The benchmarks deliberately reuse one fixed corpus
// per size so b.N iterations measure the algorithm, not data generation.
//
// Throughput convention:
//   - Delta:  b.SetBytes(len(newFile))  — input bytes processed per second.
//   - Patch:  b.SetBytes(len(newFile))  — reconstructed output bytes per
//             second. This is what users actually care about ("how fast can
//             I rebuild a file from a delta") and matches how rsync-style
//             tooling is usually reported.
//
// The standard "ns/op" column will reflect the chosen size; divide by 1e9
// for seconds per file. The "MB/s" column from -benchmem / -benchtime is
// the headline number.

const (
	sigBlockLen  uint32 = 2048
	sigStrongLen uint32 = 32
)

// sizes covers small / medium / large to expose any per-call overhead and
// the steady-state throughput. 50 MB is the largest the existing root-level
// benchmarks use; we keep parity.
var streamingSizes = []int{
	1 << 20,  // 1 MB
	10 << 20, // 10 MB
	50 << 20, // 50 MB
}

// chunkSizes covers values a real caller (e.g. an FFI consumer feeding the
// session from a network or file read loop) would plausibly use. 4 KB is a
// page; 64 KB matches io.Copy's default; 1 MB is a "whole big buffer" feed.
var benchChunkSizes = []int{
	4 << 10,  // 4 KB
	64 << 10, // 64 KB
	1 << 20,  // 1 MB
}

// makeCorpus returns (basis, modified, signature) for benchmarking.
// modified = basis with the final 10% rewritten to fresh random bytes, which
// is the same workload as BenchmarkDeltaChangeTail in the root package and
// gives the delta engine real copy+literal mix to work on.
func makeCorpus(b *testing.B, totalBytes int) (basis, modified []byte, sig *librsync.SignatureType) {
	b.Helper()

	tailBytes := totalBytes / 10
	headBytes := totalBytes - tailBytes

	basis = make([]byte, totalBytes)
	if _, err := rand.New(rand.NewSource(1)).Read(basis); err != nil {
		b.Fatalf("seed basis: %v", err)
	}

	modified = make([]byte, totalBytes)
	copy(modified, basis[:headBytes])
	if _, err := rand.New(rand.NewSource(2)).Read(modified[headBytes:]); err != nil {
		b.Fatalf("seed modified tail: %v", err)
	}

	var sigBuf bytes.Buffer
	s, err := librsync.Signature(bytes.NewReader(basis), &sigBuf, sigBlockLen, sigStrongLen, librsync.BLAKE2_SIG_MAGIC)
	if err != nil {
		b.Fatalf("signature: %v", err)
	}
	return basis, modified, s
}

// ── Delta streaming ───────────────────────────────────────────────────────────

func benchmarkDeltaStream(b *testing.B, totalBytes, chunkSize int) {
	_, modified, sig := makeCorpus(b, totalBytes)

	b.SetBytes(int64(totalBytes))
	b.ReportAllocs()
	b.ResetTimer()

	var deltaSize int
	for i := 0; i < b.N; i++ {
		sess, err := adapter.NewDeltaSession(sig)
		if err != nil {
			b.Fatalf("new delta session: %v", err)
		}

		var produced int
		for off := 0; off < totalBytes; off += chunkSize {
			end := off + chunkSize
			if end > totalBytes {
				end = totalBytes
			}
			out, err := sess.Feed(modified[off:end])
			if err != nil {
				b.Fatalf("delta feed: %v", err)
			}
			produced += len(out)
		}
		tail, err := sess.End()
		if err != nil {
			b.Fatalf("delta end: %v", err)
		}
		produced += len(tail)
		deltaSize = produced
	}

	b.ReportMetric(float64(deltaSize)/float64(totalBytes)*100, "delta%")
}

func BenchmarkDeltaStream(b *testing.B) {
	for _, sz := range streamingSizes {
		for _, ch := range benchChunkSizes {
			b.Run(fmt.Sprintf("file=%s/chunk=%s", humanBytes(sz), humanBytes(ch)), func(b *testing.B) {
				benchmarkDeltaStream(b, sz, ch)
			})
		}
	}
}

// ── Patch streaming ───────────────────────────────────────────────────────────

func benchmarkPatchStream(b *testing.B, totalBytes, chunkSize int) {
	basis, modified, sig := makeCorpus(b, totalBytes)

	// Build the delta once, outside the timed loop.
	delta, err := adapter.DeltaBytes(sig, modified)
	if err != nil {
		b.Fatalf("build delta: %v", err)
	}

	// Throughput convention: bytes of reconstructed output per second.
	b.SetBytes(int64(totalBytes))
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		sess := adapter.NewPatchSession(makeReadAt(basis))

		var produced int
		for off := 0; off < len(delta); off += chunkSize {
			end := off + chunkSize
			if end > len(delta) {
				end = len(delta)
			}
			out, err := sess.Feed(delta[off:end])
			if err != nil {
				b.Fatalf("patch feed: %v", err)
			}
			produced += len(out)
		}
		tail, err := sess.End()
		if err != nil {
			b.Fatalf("patch end: %v", err)
		}
		produced += len(tail)

		if produced != totalBytes {
			b.Fatalf("patch output size: got %d, want %d", produced, totalBytes)
		}
	}

	b.ReportMetric(float64(len(delta))/float64(totalBytes)*100, "delta%")
}

func BenchmarkPatchStream(b *testing.B) {
	for _, sz := range streamingSizes {
		for _, ch := range benchChunkSizes {
			b.Run(fmt.Sprintf("file=%s/chunk=%s", humanBytes(sz), humanBytes(ch)), func(b *testing.B) {
				benchmarkPatchStream(b, sz, ch)
			})
		}
	}
}

func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%dMB", n>>20)
	case n >= 1<<10:
		return fmt.Sprintf("%dKB", n>>10)
	default:
		return fmt.Sprintf("%dB", n)
	}
}
