package adapter_test

import (
	"fmt"
	"testing"

	"github.com/balena-os/librsync-go/ffi/adapter"
	"github.com/stretchr/testify/require"
)

// streamFeedInto consumes data via a (input, dst) → (n, morePending, err)
// signature, draining the session into dst-sized output buffers. The first
// call per input chunk consumes the input; subsequent loop iterations pass
// empty input as a pure drain request, looping while morePending is true.
func streamFeedInto(
	t *testing.T,
	data []byte,
	chunkSize, outCap int,
	feedInto func(input, dst []byte) (int, bool, error),
) [][]byte {
	t.Helper()
	dst := make([]byte, outCap)
	var chunks [][]byte
	for i := 0; i < len(data); i += chunkSize {
		end := i + chunkSize
		if end > len(data) {
			end = len(data)
		}
		first := true
		for {
			var input []byte
			if first {
				input = data[i:end]
				first = false
			}
			n, more, err := feedInto(input, dst)
			require.NoError(t, err)
			if n > 0 {
				cp := make([]byte, n)
				copy(cp, dst[:n])
				chunks = append(chunks, cp)
			}
			if !more {
				break
			}
		}
	}
	return chunks
}

// drainEndInto repeatedly calls endInto(dst) until morePending is false,
// collecting all chunks. Needed because EndInto returns at most len(dst)
// bytes per call and the final flush of large pending output may exceed that.
func drainEndInto(
	t *testing.T,
	outCap int,
	endInto func(dst []byte) (int, bool, error),
) [][]byte {
	t.Helper()
	dst := make([]byte, outCap)
	var chunks [][]byte
	for {
		n, more, err := endInto(dst)
		require.NoError(t, err)
		if n > 0 {
			cp := make([]byte, n)
			copy(cp, dst[:n])
			chunks = append(chunks, cp)
		}
		if !more {
			break
		}
	}
	return chunks
}

// ── SignatureSession.FeedInto / EndInto ──────────────────────────────────────

func TestSignatureFeedIntoMatchesBatch(t *testing.T) {
	for _, tt := range allTestCases {
		file, magic, blockLen, strongLen, err := parseTestName(tt)
		require.NoError(t, err)

		input := readFile(t, testdataDir+file+".old")
		want, err := adapter.SignatureBytes(input, blockLen, strongLen, magic)
		require.NoError(t, err)

		// Sweep small / mid / large output capacities to flush the
		// "leftover stays buffered for next call" path.
		for _, outCap := range []int{16, 256, 4096, 1 << 20} {
			for _, chunkSize := range streamingChunkSizes {
				t.Run(fmt.Sprintf("%s/chunk%d/outCap%d", tt, chunkSize, outCap), func(t *testing.T) {
					sess, err := adapter.NewSignatureSession(blockLen, strongLen, magic)
					require.NoError(t, err)

					chunks := streamFeedInto(t, input, chunkSize, outCap, sess.FeedInto)
					chunks = append(chunks, drainEndInto(t, outCap, sess.EndInto)...)

					require.Equal(t, want, collectChunks(chunks))
				})
			}
		}
	}
}

// ── DeltaSession.FeedInto / EndInto ──────────────────────────────────────────

func TestDeltaFeedIntoMatchesBatch(t *testing.T) {
	for _, tt := range allTestCases {
		file, _, _, _, err := parseTestName(tt)
		require.NoError(t, err)

		newData := readFile(t, testdataDir+file+".new")
		refSig := readFile(t, testdataDir+tt+".signature")

		sig, err := adapter.ParseSignature(refSig)
		require.NoError(t, err)
		want, err := adapter.DeltaBytes(sig, newData)
		require.NoError(t, err)

		for _, outCap := range []int{16, 256, 4096, 1 << 20} {
			for _, chunkSize := range streamingChunkSizes {
				t.Run(fmt.Sprintf("%s/chunk%d/outCap%d", tt, chunkSize, outCap), func(t *testing.T) {
					sigStream, err := adapter.ParseSignature(refSig)
					require.NoError(t, err)
					sess, err := adapter.NewDeltaSession(sigStream)
					require.NoError(t, err)

					chunks := streamFeedInto(t, newData, chunkSize, outCap, sess.FeedInto)
					chunks = append(chunks, drainEndInto(t, outCap, sess.EndInto)...)

					require.Equal(t, want, collectChunks(chunks))
				})
			}
		}
	}
}

// ── PatchSession.FeedInto / EndInto ──────────────────────────────────────────

func TestPatchFeedIntoMatchesBatch(t *testing.T) {
	for _, tt := range allTestCases {
		t.Run(tt, func(t *testing.T) {
			file, magic, blockLen, strongLen, err := parseTestName(tt)
			require.NoError(t, err)

			oldData := readFile(t, testdataDir+file+".old")
			newData := readFile(t, testdataDir+file+".new")

			sigBytes, err := adapter.SignatureBytes(oldData, blockLen, strongLen, magic)
			require.NoError(t, err)
			sig, err := adapter.ParseSignature(sigBytes)
			require.NoError(t, err)
			deltaBytes, err := adapter.DeltaBytes(sig, newData)
			require.NoError(t, err)

			for _, outCap := range []int{16, 256, 4096, 1 << 20} {
				for _, chunkSize := range []int{64, 256, 4096} {
					t.Run(fmt.Sprintf("chunk%d/outCap%d", chunkSize, outCap), func(t *testing.T) {
						sess := adapter.NewPatchSession(makeReadAt(oldData))
						chunks := streamFeedInto(t, deltaBytes, chunkSize, outCap, sess.FeedInto)
						chunks = append(chunks, drainEndInto(t, outCap, sess.EndInto)...)
						require.Equal(t, newData, collectChunks(chunks))
					})
				}
			}
		})
	}
}
