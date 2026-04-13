// Package testhelper provides C-heap rs_read_seeker_t fixtures for cbridge tests.
// It wraps CGo so the test files themselves do not need to import "C".
package testhelper

/*
#include <stdint.h>
#include <stddef.h>
#include <stdlib.h>
#include <string.h>

// Duplicate the rs_read_seeker_t layout.  The struct is identical to the one
// in the parent package; both packages use unsafe.Pointer to bridge between
// them so no CGo-type sharing is required.
typedef struct {
    void*    userdata;
    int32_t  (*read_at)(void* userdata, int64_t offset,
                        uint8_t* buf, size_t len, size_t* bytes_read);
} rs_read_seeker_t;

// test_buf_t is the userdata for ok_read_at: a C-heap byte slice.
typedef struct {
    uint8_t* data;
    size_t   len;
} test_buf_t;

// ok_read_at performs a bounds-checked pread into test_buf_t.
// Returns 0 with *out == 0 at EOF (the cbridge caller will translate to io.EOF).
static int32_t ok_read_at(void* userdata, int64_t offset,
                           uint8_t* buf, size_t req, size_t* out) {
    const test_buf_t* tb = (const test_buf_t*)userdata;
    if (offset < 0 || (size_t)offset >= tb->len) {
        *out = 0;
        return 0;
    }
    size_t avail = tb->len - (size_t)offset;
    size_t n     = req < avail ? req : avail;
    memcpy(buf, tb->data + offset, n);
    *out = n;
    return 0;
}

// err_read_at always returns -2 (LIBRSYNC_ERR_CORRUPT) for error-path tests.
static int32_t err_read_at(void* userdata, int64_t offset,
                            uint8_t* buf, size_t req, size_t* out) {
    (void)userdata; (void)offset; (void)buf; (void)req;
    *out = 0;
    return -2;
}

// Setters that install a function pointer without unsafe Go casts.
static void rs_set_ok(rs_read_seeker_t* rs)  { rs->read_at = ok_read_at;  }
static void rs_set_err(rs_read_seeker_t* rs) { rs->read_at = err_read_at; }
*/
import "C"

import "unsafe"

// NewOkReadSeeker allocates a C-heap rs_read_seeker_t backed by a copy of
// data. The returned cleanup func frees all C-heap allocations; it must be
// called exactly once when the seeker is no longer needed.
func NewOkReadSeeker(data []byte) (rs unsafe.Pointer, cleanup func()) {
	tb := (*C.test_buf_t)(C.malloc(C.size_t(unsafe.Sizeof(C.test_buf_t{}))))
	if len(data) > 0 {
		tb.data = (*C.uint8_t)(C.CBytes(data))
	} else {
		tb.data = nil
	}
	tb.len = C.size_t(len(data))

	crs := (*C.rs_read_seeker_t)(C.malloc(C.size_t(unsafe.Sizeof(C.rs_read_seeker_t{}))))
	crs.userdata = unsafe.Pointer(tb)
	C.rs_set_ok(crs)

	cleanup = func() {
		if tb.data != nil {
			C.free(unsafe.Pointer(tb.data))
		}
		C.free(unsafe.Pointer(tb))
		C.free(unsafe.Pointer(crs))
	}
	return unsafe.Pointer(crs), cleanup
}

// NewErrReadSeeker allocates a C-heap rs_read_seeker_t whose read_at always
// returns -2 (LIBRSYNC_ERR_CORRUPT). Useful for error-propagation tests.
// The returned cleanup func frees the allocation.
func NewErrReadSeeker() (rs unsafe.Pointer, cleanup func()) {
	crs := (*C.rs_read_seeker_t)(C.malloc(C.size_t(unsafe.Sizeof(C.rs_read_seeker_t{}))))
	crs.userdata = nil
	C.rs_set_err(crs)

	cleanup = func() {
		C.free(unsafe.Pointer(crs))
	}
	return unsafe.Pointer(crs), cleanup
}
