// Package main is a CGo package that exports the librsync C ABI.
// Build as a shared library: go build -buildmode=c-shared -o librsync.so .
// Build as a static library: go build -buildmode=c-archive -o librsync.a .
package main

/*
#include <stdint.h>
#include <stdlib.h>

// Callback struct for random-access reads on the base file.
// Dart allocates this with calloc and keeps it alive for the patch session.
typedef struct {
    void* userdata;
    int32_t (*read_at)(void* userdata, int64_t offset, uint8_t* buf, size_t len, size_t* bytes_read);
} rs_read_seeker_t;

// Trampoline so Go can call the function pointer without unsafe casts.
static int32_t call_read_at(const rs_read_seeker_t* rs, int64_t offset, uint8_t* buf, size_t len, size_t* bytes_read) {
    return rs->read_at(rs->userdata, offset, buf, len, bytes_read);
}
*/
import "C"

import (
	"sync"
	"unsafe"

	librsync "github.com/balena-os/librsync-go"
	"github.com/balena-os/librsync-go/ffi/adapter"
	"github.com/balena-os/librsync-go/ffi/internal/cbridge"
)

func main() {}

// ── Error codes ───────────────────────────────────────────────────────────────

const (
	errOK      = C.int32_t(0)
	errArgs    = C.int32_t(-1)
	errCorrupt = C.int32_t(-2)
	errMem     = C.int32_t(-3)
)

// librsync_strerror returns a static, immutable string for the given error
// code. The returned pointer is valid forever; never call librsync_free on it.
//
//export librsync_strerror
func librsync_strerror(code C.int32_t) *C.char {
	switch code {
	case errOK:
		return C.CString("ok")
	case errArgs:
		return C.CString("invalid arguments")
	case errCorrupt:
		return C.CString("corrupt or unexpected input")
	case errMem:
		return C.CString("memory allocation failed")
	default:
		return C.CString("unknown error")
	}
}

// ── Memory ────────────────────────────────────────────────────────────────────

// librsync_free frees a buffer returned by this library. Must be called
// exactly once per returned buffer. Do not call on NULL or on pointers that
// were not returned by this library.
//
//export librsync_free
func librsync_free(ptr unsafe.Pointer) {
	C.free(ptr)
}

// ── Handle registry ───────────────────────────────────────────────────────────

var (
	handles   = map[C.intptr_t]interface{}{}
	handlesMu sync.Mutex
	nextID    C.intptr_t = 1
)

func storeHandle(v interface{}) C.intptr_t {
	handlesMu.Lock()
	defer handlesMu.Unlock()
	h := nextID
	nextID++
	handles[h] = v
	return h
}

func loadHandle(h C.intptr_t) interface{} {
	handlesMu.Lock()
	defer handlesMu.Unlock()
	return handles[h]
}

func dropHandle(h C.intptr_t) {
	handlesMu.Lock()
	defer handlesMu.Unlock()
	delete(handles, h)
}

// ── Output helpers ────────────────────────────────────────────────────────────

// cInput wraps a C pointer+length as a Go byte slice without copying.
func cInput(ptr *C.uint8_t, n C.size_t) []byte {
	if n == 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(ptr)), int(n))
}

// setOutput copies data to C-heap memory and writes the pointer and length to
// the caller's output parameters. Sets both to zero when data is nil/empty.
// The caller must librsync_free the returned pointer.
func setOutput(outPtr **C.uint8_t, outLen *C.size_t, data []byte) {
	if len(data) == 0 {
		*outPtr = nil
		*outLen = 0
		return
	}
	*outPtr = (*C.uint8_t)(C.CBytes(data))
	*outLen = C.size_t(len(data))
}

// ── Batch API ─────────────────────────────────────────────────────────────────

// librsync_signature generates a serialized signature from a complete file
// buffer. The caller must librsync_free(*out_ptr).
//
//export librsync_signature
func librsync_signature(
	inputPtr *C.uint8_t, inputLen C.size_t,
	blockLen, strongLen, sigType C.uint32_t,
	outPtr **C.uint8_t, outLen *C.size_t,
) C.int32_t {
	result, err := adapter.SignatureBytes(
		cInput(inputPtr, inputLen),
		uint32(blockLen), uint32(strongLen),
		librsync.MagicNumber(sigType),
	)
	if err != nil {
		return errCorrupt
	}
	setOutput(outPtr, outLen, result)
	return errOK
}

// librsync_delta computes a delta from serialized signature bytes and a new
// file buffer. The caller must librsync_free(*out_ptr).
//
//export librsync_delta
func librsync_delta(
	sigPtr *C.uint8_t, sigLen C.size_t,
	inputPtr *C.uint8_t, inputLen C.size_t,
	outPtr **C.uint8_t, outLen *C.size_t,
) C.int32_t {
	sig, err := adapter.ParseSignature(cInput(sigPtr, sigLen))
	if err != nil {
		return errCorrupt
	}
	result, err := adapter.DeltaBytes(sig, cInput(inputPtr, inputLen))
	if err != nil {
		return errCorrupt
	}
	setOutput(outPtr, outLen, result)
	return errOK
}

// librsync_patch applies a delta to a complete base file buffer.
// The caller must librsync_free(*out_ptr).
//
//export librsync_patch
func librsync_patch(
	basePtr *C.uint8_t, baseLen C.size_t,
	deltaPtr *C.uint8_t, deltaLen C.size_t,
	outPtr **C.uint8_t, outLen *C.size_t,
) C.int32_t {
	result, err := adapter.PatchBytes(
		cInput(basePtr, baseLen),
		cInput(deltaPtr, deltaLen),
	)
	if err != nil {
		return errCorrupt
	}
	setOutput(outPtr, outLen, result)
	return errOK
}

// ── Parsed Signature Handle ───────────────────────────────────────────────────

// librsync_sig_parse parses serialized signature bytes into an in-memory
// structure. The input buffer may be freed immediately after this call.
// Returns a handle > 0 on success, 0 on failure.
//
//export librsync_sig_parse
func librsync_sig_parse(sigPtr *C.uint8_t, sigLen C.size_t) C.intptr_t {
	sig, err := adapter.ParseSignature(cInput(sigPtr, sigLen))
	if err != nil {
		return 0
	}
	return storeHandle(sig)
}

// librsync_sig_free frees a parsed signature handle.
//
//export librsync_sig_free
func librsync_sig_free(handle C.intptr_t) {
	dropHandle(handle)
}

// ── Streaming Signature ───────────────────────────────────────────────────────

// librsync_signature_new creates a streaming signature session.
// Returns a handle > 0 on success, 0 on failure.
//
//export librsync_signature_new
func librsync_signature_new(blockLen, strongLen, sigType C.uint32_t) C.intptr_t {
	sess, err := adapter.NewSignatureSession(
		uint32(blockLen), uint32(strongLen),
		librsync.MagicNumber(sigType),
	)
	if err != nil {
		return 0
	}
	return storeHandle(sess)
}

// librsync_signature_feed processes a chunk of input.
// *out_ptr/*out_len receive any output produced; may be NULL/0 if the internal
// buffer has not flushed yet. Caller must librsync_free(*out_ptr) if *out_len > 0.
// On error the handle is valid only for librsync_signature_free.
//
//export librsync_signature_feed
func librsync_signature_feed(
	handle C.intptr_t,
	inputPtr *C.uint8_t, inputLen C.size_t,
	outPtr **C.uint8_t, outLen *C.size_t,
) C.int32_t {
	sess, ok := loadHandle(handle).(*adapter.SignatureSession)
	if !ok {
		return errArgs
	}
	result, err := sess.Feed(cInput(inputPtr, inputLen))
	if err != nil {
		return errCorrupt
	}
	setOutput(outPtr, outLen, result)
	return errOK
}

// librsync_signature_end finalizes the session and returns any remaining output.
// Always invalidates the handle — do NOT call librsync_signature_free after this.
// Caller must librsync_free(*out_ptr) if *out_len > 0.
//
//export librsync_signature_end
func librsync_signature_end(
	handle C.intptr_t,
	outPtr **C.uint8_t, outLen *C.size_t,
) C.int32_t {
	sess, ok := loadHandle(handle).(*adapter.SignatureSession)
	dropHandle(handle) // always invalidate, even on error
	if !ok {
		return errArgs
	}
	result, err := sess.End()
	if err != nil {
		return errCorrupt
	}
	setOutput(outPtr, outLen, result)
	return errOK
}

// librsync_signature_free abandons the session without finalizing.
// Use on the error path only, when librsync_signature_end has not been called.
//
//export librsync_signature_free
func librsync_signature_free(handle C.intptr_t) {
	dropHandle(handle)
}

// ── Streaming Delta ───────────────────────────────────────────────────────────

// librsync_delta_new creates a streaming delta session from a parsed signature.
// sig_handle remains valid and may be reused for further delta sessions.
// Returns a handle > 0 on success, 0 on failure.
//
//export librsync_delta_new
func librsync_delta_new(sigHandle C.intptr_t) C.intptr_t {
	sig, ok := loadHandle(sigHandle).(*librsync.SignatureType)
	if !ok {
		return 0
	}
	sess, err := adapter.NewDeltaSession(sig)
	if err != nil {
		return 0
	}
	return storeHandle(sess)
}

// librsync_delta_feed processes a chunk of the new file.
// *out_ptr/*out_len may be NULL/0 if the literal buffer has not yet flushed.
// On error the handle is valid only for librsync_delta_free.
//
//export librsync_delta_feed
func librsync_delta_feed(
	handle C.intptr_t,
	inputPtr *C.uint8_t, inputLen C.size_t,
	outPtr **C.uint8_t, outLen *C.size_t,
) C.int32_t {
	sess, ok := loadHandle(handle).(*adapter.DeltaSession)
	if !ok {
		return errArgs
	}
	result, err := sess.Feed(cInput(inputPtr, inputLen))
	if err != nil {
		return errCorrupt
	}
	setOutput(outPtr, outLen, result)
	return errOK
}

// librsync_delta_end finalizes the session and flushes remaining output.
// Always invalidates the handle — do NOT call librsync_delta_free after this.
//
//export librsync_delta_end
func librsync_delta_end(
	handle C.intptr_t,
	outPtr **C.uint8_t, outLen *C.size_t,
) C.int32_t {
	sess, ok := loadHandle(handle).(*adapter.DeltaSession)
	dropHandle(handle)
	if !ok {
		return errArgs
	}
	result, err := sess.End()
	if err != nil {
		return errCorrupt
	}
	setOutput(outPtr, outLen, result)
	return errOK
}

// librsync_delta_free abandons the session without finalizing.
//
//export librsync_delta_free
func librsync_delta_free(handle C.intptr_t) {
	dropHandle(handle)
}

// ── Streaming Patch ───────────────────────────────────────────────────────────

// librsync_patch_new creates a streaming patch session. The rs_read_seeker_t
// struct and its NativeCallable must remain valid until librsync_patch_end or
// librsync_patch_free returns. Returns a handle > 0 on success, 0 on failure.
//
//export librsync_patch_new
func librsync_patch_new(rs *C.rs_read_seeker_t) C.intptr_t {
	if rs == nil || rs.read_at == nil {
		return 0
	}
	readAt := cbridge.NewCallbackReadAt(unsafe.Pointer(rs))
	sess := adapter.NewPatchSession(readAt)
	return storeHandle(sess)
}

// librsync_patch_feed buffers a chunk of the delta stream.
// On error the handle is valid only for librsync_patch_free.
//
//export librsync_patch_feed
func librsync_patch_feed(
	handle C.intptr_t,
	deltaPtr *C.uint8_t, deltaLen C.size_t,
) C.int32_t {
	sess, ok := loadHandle(handle).(*adapter.PatchSession)
	if !ok {
		return errArgs
	}
	if err := sess.Feed(cInput(deltaPtr, deltaLen)); err != nil {
		return errCorrupt
	}
	return errOK
}

// librsync_patch_end applies the buffered delta to the base file via the
// callback and returns the reconstructed output. Always invalidates the handle.
// Caller must librsync_free(*out_ptr) if *out_len > 0.
//
//export librsync_patch_end
func librsync_patch_end(
	handle C.intptr_t,
	outPtr **C.uint8_t, outLen *C.size_t,
) C.int32_t {
	sess, ok := loadHandle(handle).(*adapter.PatchSession)
	dropHandle(handle)
	if !ok {
		return errArgs
	}
	result, err := sess.End()
	if err != nil {
		return errCorrupt
	}
	setOutput(outPtr, outLen, result)
	return errOK
}

// librsync_patch_free abandons the session without applying the patch.
//
//export librsync_patch_free
func librsync_patch_free(handle C.intptr_t) {
	dropHandle(handle)
}
