package librsync

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/balena-os/circbuf"
)

type DeltaStruct struct {
	sig      *SignatureType
	match    *match
	prevByte byte
	weakSum  *Rollsum
	block    circbuf.Buffer
	output   io.Writer
	buf      []byte
}

func (d *DeltaStruct) Digest(b []byte) error {
	return d.digestReader(bufio.NewReader(bytes.NewReader(b)))
}

func (d *DeltaStruct) digestReader(input *bufio.Reader) error {
	blockLenu64 := uint64(d.sig.BlockLen)
	buf := d.buf

	for {
		if d.weakSum.count < blockLenu64 {
			// Fill phase: read as many bytes as needed to complete the block.
			n, err := input.Read(buf[:blockLenu64-d.weakSum.count])
			if n == 0 || err == io.EOF {
				break
			} else if err != nil {
				return err
			}
			d.block.Write(buf[:n])
			d.weakSum.Update(buf[:n])
			if d.weakSum.count < blockLenu64 {
				continue
			}
		} else {
			// Slide phase: advance the window by one byte using Rotate.
			in, err := input.ReadByte()
			if err == io.EOF {
				break
			} else if err != nil {
				return err
			}
			d.prevByte, _ = d.block.Get(0)
			d.block.WriteByte(in)
			if err := d.match.add(MATCH_KIND_LITERAL, uint64(d.prevByte), 1); err != nil {
				return err
			}
			d.weakSum.Rotate(d.prevByte, in)
		}

		if blockIdx, ok := d.sig.Weak2block[d.weakSum.Digest()]; ok {
			strong2, _ := CalcStrongSum(d.block.Bytes(), d.sig.SigType, d.sig.StrongLen)
			if bytes.Equal(d.sig.StrongSigs[blockIdx], strong2) {
				d.weakSum.Reset()
				d.block.Reset()
				if err := d.match.add(MATCH_KIND_COPY, uint64(blockIdx)*blockLenu64, blockLenu64); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (d *DeltaStruct) End() error {
	if err := d.match.addLiteralBytes(d.block.Bytes()); err != nil {
		return err
	}

	if err := d.match.flush(); err != nil {
		return err
	}

	return binary.Write(d.output, binary.BigEndian, OP_END)
}

func (d *DeltaStruct) BlockBytes() []byte {
	return d.block.Bytes()
}

func NewDelta(sig *SignatureType, output io.Writer, bufSize int) (*DeltaStruct, error) {
	return newDeltaWithLitBuf(sig, output, make([]byte, 0, bufSize))
}

func newDeltaWithLitBuf(sig *SignatureType, output io.Writer, litBuff []byte) (*DeltaStruct, error) {
	if len(litBuff) != 0 || cap(litBuff) == 0 {
		return nil, fmt.Errorf("bad literal buffer")
	}
	m := newMatch(output, litBuff)
	weakSum := NewRollsum()
	block, _ := circbuf.NewBuffer(int64(sig.BlockLen))

	delta := &DeltaStruct{
		sig:      sig,
		match:    &m,
		prevByte: byte(0),
		weakSum:  &weakSum,
		block:    block,
		output:   output,
		buf:      make([]byte, sig.BlockLen),
	}

	err := binary.Write(output, binary.BigEndian, DELTA_MAGIC)
	if err != nil {
		return nil, err
	}
	return delta, nil
}

func Delta(sig *SignatureType, i io.Reader, output io.Writer) error {
	buff := make([]byte, 0, OUTPUT_BUFFER_SIZE)
	return DeltaBuff(sig, i, output, buff)
}

// DeltaBuff like Delta but allows to pass literal buffer slice.
// This is useful for efficient computation of multiple deltas.
//
// The slice shall have zero size, and capacity of OUTPUT_BUFFER_SIZE.
//
// Example of usage:
//
//	var files []string
//	var litBuff = make([]byte, 0, OUTPUT_BUFFER_SIZE)
//	for _, file := range files {
//	  f, _ := os.Open(file)
//	  sig, _ := ReadSignatureFile(file + ".sig")
//	  delta, _ := os.OpenFile(file+".delta", os.O_CREATE|os.O_WRONLY, 0644)
//	  _ = DeltaBuff(sig, f, delta, litBuff)
//	}
func DeltaBuff(sig *SignatureType, i io.Reader, output io.Writer, litBuff []byte) error {
	delta, err := newDeltaWithLitBuf(sig, output, litBuff)
	if err != nil {
		return err
	}

	input := bufio.NewReader(i)
	err = delta.digestReader(input)
	if err != nil {
		return err
	}
	return delta.End()
}
