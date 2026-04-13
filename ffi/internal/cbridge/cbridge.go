// Package cbridge wraps the rs_read_seeker_t C callback struct as a Go
// adapter.ReadAtFunc. It is an internal package so the CGo types do not
// leak into public API surfaces.
//
// The CGo boundary lives here rather than in ffi.go so that this conversion
// layer can be tested independently of the //export package-main constraint.
package cbridge

/*
#include <stdlib.h>
#include "rs_read_seeker.h"

// call_read_at is a C trampoline so Go can invoke the function pointer without
// unsafe function-pointer casts in Go code.
static int32_t call_read_at(const rs_read_seeker_t* rs, int64_t offset,
                             uint8_t* buf, size_t len, size_t* bytes_read) {
    return rs->read_at(rs->userdata, offset, buf, len, bytes_read);
}
*/
import "C"

import (
	"fmt"
	"io"
	"unsafe"

	"github.com/balena-os/librsync-go/ffi/adapter"
)

// NewCallbackReadAt wraps an rs_read_seeker_t (passed as unsafe.Pointer to
// avoid sharing CGo types across package boundaries) as an adapter.ReadAtFunc.
//
// The struct pointed to by rsPtr must remain valid and unmodified for the
// lifetime of the returned function.
//
// CGo pointer rules: rsPtr must point to C-heap memory (allocated with malloc
// or equivalent), not Go-heap memory, because the function stores the pointer
// across CGo call boundaries.
func NewCallbackReadAt(rsPtr unsafe.Pointer) adapter.ReadAtFunc {
	rs := (*C.rs_read_seeker_t)(rsPtr)
	return func(offset int64, buf []byte) (int, error) {
		if len(buf) == 0 {
			return 0, nil
		}
		var bytesRead C.size_t
		ret := C.call_read_at(
			rs,
			C.int64_t(offset),
			(*C.uint8_t)(unsafe.Pointer(&buf[0])),
			C.size_t(len(buf)),
			&bytesRead,
		)
		n := int(bytesRead)
		if ret != 0 {
			return n, fmt.Errorf("librsync: read_at callback returned error %d", ret)
		}
		if n == 0 {
			return 0, io.EOF
		}
		return n, nil
	}
}
