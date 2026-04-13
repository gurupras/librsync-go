package cbridge

// This file tests the CGo boundary: specifically that NewCallbackReadAt
// correctly invokes the C function pointer in rs_read_seeker_t, passes
// arguments correctly across the CGo boundary, and handles all return
// paths (normal read, partial read at EOF, error return, zero-byte return).
//
// C-heap allocation and callback fixtures live in the testhelper sub-package
// so that this file does not need to import "C" (cgo in _test.go is not
// supported by the Go toolchain).

import (
	"io"
	"os"
	"testing"

	librsync "github.com/balena-os/librsync-go"
	"github.com/balena-os/librsync-go/ffi/adapter"
	"github.com/balena-os/librsync-go/ffi/internal/cbridge/testhelper"
	"github.com/stretchr/testify/require"
)

// TestCallbackReadAt_SequentialReads verifies that the callback is invoked
// correctly for sequential forward reads and that data is returned intact.
func TestCallbackReadAt_SequentialReads(t *testing.T) {
	data := []byte("abcdefghijklmnopqrstuvwxyz0123456789")

	rs, cleanup := testhelper.NewOkReadSeeker(data)
	defer cleanup()

	readAt := NewCallbackReadAt(rs)

	var got []byte
	buf := make([]byte, 8)
	for off := int64(0); off < int64(len(data)); off += 8 {
		remaining := int64(len(data)) - off
		if remaining < 8 {
			buf = buf[:remaining]
		}
		n, err := readAt(off, buf)
		require.NoError(t, err)
		require.Equal(t, len(buf), n)
		got = append(got, buf[:n]...)
	}
	require.Equal(t, data, got)
}

// TestCallbackReadAt_RandomAccess verifies that out-of-order seeks are handled
// correctly. This simulates Patch's behaviour when COPY operations reference
// non-sequential regions of the base file.
func TestCallbackReadAt_RandomAccess(t *testing.T) {
	data := make([]byte, 256)
	for i := range data {
		data[i] = byte(i)
	}

	rs, cleanup := testhelper.NewOkReadSeeker(data)
	defer cleanup()

	readAt := NewCallbackReadAt(rs)

	type want struct {
		offset int64
		length int
	}
	cases := []want{
		{200, 20},
		{0, 10},
		{128, 32},
		{50, 5},
		{240, 16},
	}
	for _, c := range cases {
		buf := make([]byte, c.length)
		n, err := readAt(c.offset, buf)
		require.NoError(t, err, "offset=%d len=%d", c.offset, c.length)
		require.Equal(t, c.length, n)
		require.Equal(t, data[c.offset:int(c.offset)+c.length], buf)
	}
}

// TestCallbackReadAt_EOF verifies that a read at or past the end of the buffer
// is signalled as io.EOF (zero bytes returned from callback → Go sets io.EOF).
func TestCallbackReadAt_EOF(t *testing.T) {
	data := []byte("short")

	rs, cleanup := testhelper.NewOkReadSeeker(data)
	defer cleanup()

	readAt := NewCallbackReadAt(rs)

	buf := make([]byte, 8)

	// Exact end of buffer.
	n, err := readAt(int64(len(data)), buf)
	require.Equal(t, 0, n)
	require.Equal(t, io.EOF, err)

	// Past the end.
	n, err = readAt(int64(len(data))+100, buf)
	require.Equal(t, 0, n)
	require.Equal(t, io.EOF, err)
}

// TestCallbackReadAt_PartialReadAtEnd verifies that a read starting before EOF
// but extending past it returns the available bytes without an error.
func TestCallbackReadAt_PartialReadAtEnd(t *testing.T) {
	data := []byte("hello")

	rs, cleanup := testhelper.NewOkReadSeeker(data)
	defer cleanup()

	readAt := NewCallbackReadAt(rs)

	// Request 10 bytes starting 3 bytes from the end → expect 2 bytes back.
	buf := make([]byte, 10)
	n, err := readAt(3, buf)
	require.NoError(t, err)
	require.Equal(t, 2, n)
	require.Equal(t, []byte("lo"), buf[:n])
}

// TestCallbackReadAt_ErrorPropagation verifies that a non-zero return code from
// the C callback surfaces as a Go error rather than silently succeeding.
func TestCallbackReadAt_ErrorPropagation(t *testing.T) {
	rs, cleanup := testhelper.NewErrReadSeeker()
	defer cleanup()

	readAt := NewCallbackReadAt(rs)

	buf := make([]byte, 8)
	_, err := readAt(0, buf)
	require.Error(t, err)
	require.Contains(t, err.Error(), "-2")
}

// TestCallbackReadAt_EmptyBuffer verifies that calling with a zero-length
// buffer is a safe no-op and does not invoke the C callback.
func TestCallbackReadAt_EmptyBuffer(t *testing.T) {
	// Use the error callback — if it were invoked it would return an error,
	// letting us detect whether the C callback was called at all.
	rs, cleanup := testhelper.NewErrReadSeeker()
	defer cleanup()

	readAt := NewCallbackReadAt(rs)
	n, err := readAt(0, []byte{})
	require.NoError(t, err)
	require.Equal(t, 0, n)
}

// TestCallbackReadAt_PatchRoundTrip is the end-to-end test for the full CGo
// call chain: C callback → NewCallbackReadAt → adapter.PatchWithCallback
// → librsync.Patch → io.ReadSeeker. It uses real testdata so any misalignment
// in the argument passing or offset arithmetic would produce wrong output.
func TestCallbackReadAt_PatchRoundTrip(t *testing.T) {
	const testdataDir = "../../../testdata/"

	cases := []struct {
		name      string
		sigType   librsync.MagicNumber
		blockLen  uint32
		strongLen uint32
	}{
		{"000-blake2-512-32", librsync.BLAKE2_SIG_MAGIC, 512, 32},
		{"003-md4-1024-13", librsync.MD4_SIG_MAGIC, 1024, 13},
		{"009-blake2-2048-26", librsync.BLAKE2_SIG_MAGIC, 2048, 26},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prefix := tc.name[:3]
			oldData, err := os.ReadFile(testdataDir + prefix + ".old")
			require.NoError(t, err)
			newData, err := os.ReadFile(testdataDir + prefix + ".new")
			require.NoError(t, err)

			sigBytes, err := adapter.SignatureBytes(oldData, tc.blockLen, tc.strongLen, tc.sigType)
			require.NoError(t, err)
			sig, err := adapter.ParseSignature(sigBytes)
			require.NoError(t, err)
			deltaBytes, err := adapter.DeltaBytes(sig, newData)
			require.NoError(t, err)

			rs, cleanup := testhelper.NewOkReadSeeker(oldData)
			defer cleanup()

			readAt := NewCallbackReadAt(rs)
			got, err := adapter.PatchWithCallback(readAt, deltaBytes)
			require.NoError(t, err)
			require.Equal(t, newData, got)
		})
	}
}
