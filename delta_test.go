package librsync

import (
	"bytes"
	"io"
	"math/rand"
	"testing"
	"time"
)

// Simple test for Delta generation: just checks if it runs without error.
//
// It is not worthwhile to compare our deltas with the deltas generated by the
// original rdiff from the C librsync: the exact delta will depend on internal
// factors like the size of the buffers used.
//
// We do however, make sure that we can apply the deltas generated by the
// original rdiff and that our combination of delta and patch produce the
// expected results. This is done in the Patch tests.
func TestDeltaSmokeTest(t *testing.T) {
	var totalBytes int64 = 1_000_000 // 1 MB

	var srcBuf bytes.Buffer
	src := io.TeeReader(
		io.LimitReader(rand.New(rand.NewSource(time.Now().UnixNano())), totalBytes),
		&srcBuf)

	s := testSignature(t, src)

	var buf bytes.Buffer

	// create 10% of difference by appending new random data
	newBytes := totalBytes / 10
	srcBuf.Truncate(int(totalBytes - newBytes))
	_, err := io.CopyN(&srcBuf, rand.New(rand.NewSource(time.Now().UnixNano())), newBytes)
	if err != nil {
		t.Error(err)
	}

	if err := Delta(s, &srcBuf, &buf); err != nil {
		t.Error(err)
	}
}
