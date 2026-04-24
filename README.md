# librsync-go

A pure-Go reimplementation of [librsync](https://github.com/librsync/librsync), providing
rsync-compatible signature, delta, and patch operations. Also ships a C ABI via CGo so the
library can be consumed from Dart, Flutter, Python, Rust, and any other language that can
call into a shared or static library.

---

## Contents

- [Go library](#go-library)
  - [Core package](#core-package)
  - [FFI adapter (Go layer)](#ffi-adapter-go-layer)
- [FFI — C ABI](#ffi--c-abi)
  - [Building](#building)
  - [Error codes](#error-codes)
  - [Memory management](#memory-management)
  - [Batch API](#batch-api)
  - [Parsed-signature handle](#parsed-signature-handle)
  - [Streaming API](#streaming-api)
  - [Streaming patch memory model](#streaming-patch-memory-model)
- [CLI (rdiff)](#cli-rdiff)
- [Benchmarks](#benchmarks)
- [Contributing](#contributing)

---

## Go library

### Core package

Import path: `github.com/balena-os/librsync-go`

These are the fundamental io-stream operations. All three accept `io.Reader`/`io.Writer`
so they stream data without loading full file contents into memory.

```go
// Signature computes a rolling-checksum signature of r and writes it to w.
func Signature(r io.Reader, w io.Writer, blockLen, strongLen uint32, sigType MagicNumber) (int, error)

// ReadSignature parses a serialized signature from r into an in-memory structure.
func ReadSignature(r io.Reader) (*SignatureType, error)

// Delta computes a delta between a new file (i) and a parsed signature, writing
// the result to output. Only changed regions appear in the delta.
func Delta(sig *SignatureType, i io.Reader, output io.Writer) error

// Patch applies a delta to a base file (base) and writes the reconstructed file
// to out. base must implement io.ReadSeeker because COPY ops seek non-sequentially.
func Patch(base io.ReadSeeker, delta io.Reader, out io.Writer) error
```

Signature type constants:

```go
librsync.BLAKE2_SIG_MAGIC // preferred; supported since librsync 1.0
librsync.MD4_SIG_MAGIC    // legacy; deprecated due to security issues
```

### FFI adapter (Go layer)

Import path: `github.com/balena-os/librsync-go/ffi/adapter`

A higher-level Go API built on top of the core package. Provides both batch (full-buffer)
and streaming (chunk-at-a-time) variants for all three operations. Used internally by the
C ABI layer and directly usable from Go.

#### Batch

```go
// SignatureBytes generates a serialized signature from a complete file buffer.
func SignatureBytes(input []byte, blockLen, strongLen uint32, sigType librsync.MagicNumber) ([]byte, error)

// ParseSignature parses serialized signature bytes into an in-memory structure.
// The input bytes may be freed after this call returns.
func ParseSignature(sigBytes []byte) (*librsync.SignatureType, error)

// DeltaBytes computes a delta between a new file buffer and a parsed signature.
func DeltaBytes(sig *librsync.SignatureType, input []byte) ([]byte, error)

// PatchBytes applies a delta to a complete base file buffer.
func PatchBytes(base, deltaBytes []byte) ([]byte, error)

// PatchWithCallback applies a delta to a base file accessed through a ReadAtFunc.
// The base file does not need to be fully loaded into memory.
func PatchWithCallback(readAt ReadAtFunc, deltaBytes []byte) ([]byte, error)
```

`ReadAtFunc` is `func(offset int64, buf []byte) (int, error)` — a random-read callback that
lets callers back the base file with an OS file handle, mmap, or any other source.

#### Streaming signature

```go
sess, err := adapter.NewSignatureSession(blockLen, strongLen, sigType)

// Feed processes a chunk of the file. Returns any signature bytes produced
// (nil if no complete block has accumulated yet).
out, err := sess.Feed(chunk)

// End flushes the final partial block. Must be called exactly once.
out, err := sess.End()
```

#### Streaming delta

```go
sess, err := adapter.NewDeltaSession(sig) // sig from ParseSignature

// Feed processes a chunk of the new file. Returns any delta bytes produced.
out, err := sess.Feed(chunk)

// End flushes remaining literals and writes the OP_END marker.
out, err := sess.End()
```

#### Streaming patch

```go
sess := adapter.NewPatchSession(readAt) // readAt: ReadAtFunc for the base file

// Feed sends a delta chunk. Returns whatever reconstructed bytes are ready.
// May return nil — output is guaranteed to be complete after End.
out, err := sess.Feed(deltaChunk)

// End signals end-of-delta and returns all remaining output.
out, err := sess.End()

// Close abandons the session cleanly without calling End.
// Blocks briefly to drain the background goroutine. Call on error paths.
sess.Close()
```

`PatchSession` runs `librsync.Patch` in a background goroutine connected by pipes so that
neither the delta stream nor the output is ever fully buffered — see
[Streaming patch memory model](#streaming-patch-memory-model).

---

## FFI — C ABI

The `ffi/` package exports a C ABI suitable for building a shared or static library. It is
the integration point for Dart/Flutter (via `dart:ffi`), Python (via `ctypes`/`cffi`), Rust
(via bindgen), and similar.

### Building

```sh
# Shared library (.so / .dylib / .dll)
cd ffi
go build -buildmode=c-shared -o librsync.so .

# Static library (.a)
go build -buildmode=c-archive -o librsync.a .
```

Both commands also emit a `librsync.h` C header with all exported declarations.

### Error codes

All functions that can fail return an `int32_t`:

| Code | Meaning |
|------|---------|
| `0`  | Success |
| `-1` | Invalid arguments (null pointer, bad handle, …) |
| `-2` | Corrupt or unexpected input |
| `-3` | Memory allocation failure |

```c
const char* librsync_strerror(int32_t code); // returns a static string
```

### Memory management

Every `uint8_t*` returned by this library is allocated on the C heap and must be freed
exactly once with:

```c
void librsync_free(void* ptr);
```

Never call `free()` on these pointers directly. Never call `librsync_free` on a pointer that
was not returned by this library or on `NULL`.

### Batch API

Single-call, full-buffer operations. Convenient for small files; for large files use the
[streaming API](#streaming-api).

```c
// Compute a signature from a complete file buffer.
// Caller must librsync_free(*out_ptr).
int32_t librsync_signature(
    const uint8_t* input_ptr, size_t input_len,
    uint32_t block_len, uint32_t strong_len, uint32_t sig_type,
    uint8_t** out_ptr, size_t* out_len);

// Compute a delta given serialized signature bytes and a new file buffer.
// Caller must librsync_free(*out_ptr).
int32_t librsync_delta(
    const uint8_t* sig_ptr,   size_t sig_len,
    const uint8_t* input_ptr, size_t input_len,
    uint8_t** out_ptr, size_t* out_len);

// Apply a delta to a complete base file buffer.
// Caller must librsync_free(*out_ptr).
int32_t librsync_patch(
    const uint8_t* base_ptr,  size_t base_len,
    const uint8_t* delta_ptr, size_t delta_len,
    uint8_t** out_ptr, size_t* out_len);
```

`sig_type` is one of:

| Constant | Value | Notes |
|----------|-------|-------|
| BLAKE2   | `0x72730137` | Preferred |
| MD4      | `0x72730136` | Legacy, deprecated |

### Parsed-signature handle

Parse a signature once and reuse it across multiple delta sessions without re-parsing.

```c
// Parse serialized signature bytes. Returns a handle > 0 on success, 0 on failure.
// Input bytes may be freed immediately after this call.
intptr_t librsync_sig_parse(const uint8_t* sig_ptr, size_t sig_len);

// Free the parsed signature handle.
void librsync_sig_free(intptr_t handle);
```

### Streaming API

All three operations support a `new` / `feed` / `end` / `free` lifecycle. Feed chunks of
arbitrary size; each `feed` call returns however much output has been produced so far (may
be zero). `end` flushes any remaining output. `free` abandons a session without finalizing.

#### Streaming signature

```c
// Create a session. Returns handle > 0 on success, 0 on failure.
intptr_t librsync_signature_new(uint32_t block_len, uint32_t strong_len, uint32_t sig_type);

// Feed a chunk of the file. *out_ptr/*out_len receive any output produced.
// Caller must librsync_free(*out_ptr) if *out_len > 0.
int32_t librsync_signature_feed(
    intptr_t handle,
    const uint8_t* input_ptr, size_t input_len,
    uint8_t** out_ptr, size_t* out_len);

// Finalize. Returns remaining output. Invalidates the handle.
// Do NOT call librsync_signature_free after this.
// Caller must librsync_free(*out_ptr) if *out_len > 0.
int32_t librsync_signature_end(
    intptr_t handle,
    uint8_t** out_ptr, size_t* out_len);

// Abandon without finalizing. Use on error paths only.
void librsync_signature_free(intptr_t handle);
```

#### Streaming delta

```c
// Create a session from a parsed signature handle.
// sig_handle remains valid and may be reused for further delta sessions.
intptr_t librsync_delta_new(intptr_t sig_handle);

// Feed a chunk of the new file. *out_ptr/*out_len receive any delta output produced.
// Caller must librsync_free(*out_ptr) if *out_len > 0.
int32_t librsync_delta_feed(
    intptr_t handle,
    const uint8_t* input_ptr, size_t input_len,
    uint8_t** out_ptr, size_t* out_len);

// Finalize. Returns remaining output and OP_END. Invalidates the handle.
// Do NOT call librsync_delta_free after this.
// Caller must librsync_free(*out_ptr) if *out_len > 0.
int32_t librsync_delta_end(
    intptr_t handle,
    uint8_t** out_ptr, size_t* out_len);

// Abandon without finalizing.
void librsync_delta_free(intptr_t handle);
```

#### Streaming patch

The base file is accessed through a callback struct rather than a buffer, so it is never
loaded into memory. Declare the struct and populate it before creating a session:

```c
typedef struct {
    void* userdata;
    // Read up to `len` bytes from the base file at absolute `offset` into `buf`.
    // Write the number of bytes read to *bytes_read.
    // Return 0 on success, non-zero on error. Return 0 with *bytes_read = 0 for EOF.
    int32_t (*read_at)(void* userdata, int64_t offset,
                       uint8_t* buf, size_t len, size_t* bytes_read);
} rs_read_seeker_t;
```

```c
// Create a session. rs must remain valid until patch_end or patch_free returns.
// Returns handle > 0 on success, 0 on failure.
intptr_t librsync_patch_new(const rs_read_seeker_t* rs);

// Feed a chunk of the delta stream. *out_ptr/*out_len receive any reconstructed
// bytes produced so far. May be NULL/0 — output is complete after patch_end.
// Caller must librsync_free(*out_ptr) if *out_len > 0.
int32_t librsync_patch_feed(
    intptr_t handle,
    const uint8_t* delta_ptr, size_t delta_len,
    uint8_t** out_ptr, size_t* out_len);

// Finalize. Returns all remaining reconstructed bytes. Invalidates the handle.
// Do NOT call librsync_patch_free after this.
// Caller must librsync_free(*out_ptr) if *out_len > 0.
int32_t librsync_patch_end(
    intptr_t handle,
    uint8_t** out_ptr, size_t* out_len);

// Abandon without finalizing. Blocks briefly to drain the background goroutine.
void librsync_patch_free(intptr_t handle);
```

**Typical usage:**

```c
rs_read_seeker_t rs = { .userdata = my_file, .read_at = my_read_at };
intptr_t h = librsync_patch_new(&rs);

uint8_t* out; size_t out_len;
while ((n = read_next_delta_chunk(buf, sizeof(buf))) > 0) {
    if (librsync_patch_feed(h, buf, n, &out, &out_len) != 0) goto error;
    if (out_len > 0) { write_output(out, out_len); librsync_free(out); }
}

if (librsync_patch_end(h, &out, &out_len) != 0) goto error;
if (out_len > 0) { write_output(out, out_len); librsync_free(out); }
return;

error:
librsync_patch_free(h); // safe to call even after patch_feed error
```

### Streaming patch memory model

| Resource | Bound |
|----------|-------|
| Delta bytes in transit | O(1) — `io.Pipe`, zero internal buffer |
| Output in flight | ≤ ~1 MB — 32-slot channel, ≤ 32 KB per slot |
| Base file | O(1) — random-read callback, never loaded |

The entire reconstructed file is never held in memory simultaneously. Each `patch_feed` call
returns output as it is produced; by the time `patch_end` is called only the tail of the
output (from the last partially-consumed delta op) remains to be flushed.

---

## CLI (rdiff)

```sh
go install github.com/balena-os/librsync-go/cmd/rdiff@latest
```

```
rdiff signature [options] basis-file sig-file
rdiff delta      sig-file new-file  delta-file
rdiff patch      basis-file delta-file new-file
```

---

## Benchmarks

```sh
go test -bench=. -benchtime=5x -count=6 .
```

To compare before and after a change:

```sh
go test -bench=. -benchtime=5x -count=6 . > old.txt
# make your change
go test -bench=. -benchtime=5x -count=6 . > new.txt
go install golang.org/x/perf/cmd/benchstat@latest
benchstat old.txt new.txt
```

The suite covers signature generation and delta computation (change tail/head, append,
prepend, insert, cut) at 1 MB and 50 MB scales.

---

## Contributing

If you're interested in contributing, that's awesome!

### Pull requests

- We use [Versionist](https://github.com/product-os/versionist) to manage versioning and
  generate the changelog.
- At least one commit in a PR should have a `Change-Type: type` footer, where `type` is
  `patch`, `minor`, or `major`. The subject of that commit appears in the changelog.
- Commits should be squashed as much as makes sense.
- Commits should be signed-off (`git commit -s`).
