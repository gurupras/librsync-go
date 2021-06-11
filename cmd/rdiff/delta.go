package main

import (
	"io/ioutil"
	"os"

	"github.com/balena-os/librsync-go"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli"
)

var (
	magic            = librsync.BLAKE2_SIG_MAGIC
	blockLen  uint32 = 512
	strongLen uint32 = 32
	bufSize          = 65536
)

func CommandDelta(c *cli.Context) {
	if len(c.Args()) > 3 {
		logrus.Warnf("%d additional arguments passed are ignored", len(c.Args())-2)
	}

	if c.Args().Get(0) == "" {
		logrus.Fatalf("Missing basis file")
	}

	if c.Args().Get(1) == "" {
		logrus.Fatalf("Missing delta file")
	}
	if c.Args().Get(2) == "" {
		logrus.Fatalf("Missing newfile file")
	}

	basis, err := os.Open(c.Args().Get(0))
	if err != nil {
		logrus.Fatal(err)
	}
	defer basis.Close()

	delta, err := os.OpenFile(c.Args().Get(1), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(0600))
	if err != nil {
		logrus.Fatal(err)
	}
	defer delta.Close()

	newfile, err := os.Open(c.Args().Get(2))
	if err != nil {
		logrus.Fatal(err)
	}
	defer newfile.Close()

	signature, err := librsync.Signature(basis, ioutil.Discard, blockLen, strongLen, magic)
	if err != nil {
		panic(err)
	}

	err = librsync.Delta(signature, newfile, delta)
	if err != nil {
		panic(err)
	}

}
