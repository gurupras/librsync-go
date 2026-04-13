#ifndef RS_READ_SEEKER_H
#define RS_READ_SEEKER_H

#include <stdint.h>
#include <stddef.h>

/*
 * rs_read_seeker_t is the C struct Dart provides to give Go random-access
 * reads into the base file without loading it entirely into memory.
 *
 * read_at must read exactly len bytes from offset into buf, setting
 * *bytes_read to the number of bytes actually read. Partial reads are only
 * permitted at EOF. Returns 0 (LIBRSYNC_OK) on success, negative on error.
 * A zero *bytes_read with a 0 return code signals EOF.
 */
typedef struct {
    void*   userdata;
    int32_t (*read_at)(void*    userdata,
                       int64_t  offset,
                       uint8_t* buf,
                       size_t   len,
                       size_t*  bytes_read);
} rs_read_seeker_t;

#endif /* RS_READ_SEEKER_H */
