package main

import (
	"math/rand"

	librsyncjs "github.com/balena-os/librsync-go/js"
)

func main() {
	rand.Seed(0)
	librsyncjs.Export()
	done := make(chan struct{}, 0)
	<-done
}
