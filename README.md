# librsync-go

librsync-go is a reimplementation of [librsync](https://github.com/librsync/librsync) in Go.

## Installing

To install the rdiff utility:

```sh
go install github.com/balena-os/librsync-go/cmd/rdiff
```

To use it as a library simply include `github.com/balena-os/librsync-go` in your import statement

## Benchmarks

To run the benchmarks:

```sh
go test -bench=. -benchtime=5x -count=6 .
```

To compare before and after a change, save results to files and use
[benchstat](https://pkg.go.dev/golang.org/x/perf/cmd/benchstat):

```sh
# on the baseline
go test -bench=. -benchtime=5x -count=6 . > old.txt

# after your change
go test -bench=. -benchtime=5x -count=6 . > new.txt

benchstat old.txt new.txt
```

Install `benchstat` if needed:

```sh
go install golang.org/x/perf/cmd/benchstat@latest
```

The benchmark suite covers signature generation and delta computation
(change tail/head, append, prepend, insert, cut) at 1MB and 50MB scales.

## Contributing

If you're interested in contributing, that's awesome!

### Pull requests

Here's a few guidelines to make the process easier for everyone involved.

- We use [Versionist](https://github.com/product-os/versionist) to manage
  versioning (and in particular, [semantic versioning](https://semver.org)) and
  generate the changelog for this project.
- At least one commit in a PR should have a `Change-Type: type` footer, where
  `type` can be `patch`, `minor` or `major`. The subject of this commit will be
  added to the changelog.
- Commits should be squashed as much as makes sense.
- Commits should be signed-off (`git commit -s`)
