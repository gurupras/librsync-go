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
}

func (d *DeltaStruct) Digest(b []byte) error {
	buf := bytes.NewBuffer(b)
	return d.digestReader(buf)
}

func (d *DeltaStruct) digestReader(input io.ByteReader) error {
	for {
		in, err := input.ReadByte()
		if err == io.EOF {
			break
		} else if err != nil {
			return err
		}
		if d.block.TotalWritten() > 0 {
			d.prevByte, err = d.block.Get(0)
			if err != nil {
				return err
			}
		}
		d.block.WriteByte(in)
		d.weakSum.Rollin(in)

		if d.weakSum.count < uint64(d.sig.BlockLen) {
			continue
		}

		if d.weakSum.count > uint64(d.sig.BlockLen) {
			err := d.match.add(MATCH_KIND_LITERAL, uint64(d.prevByte), 1)
			if err != nil {
				return err
			}
			d.weakSum.Rollout(d.prevByte)
		}

		if blockIdx, ok := d.sig.Weak2block[d.weakSum.Digest()]; ok {
			strong2, _ := CalcStrongSum(d.block.Bytes(), d.sig.SigType, d.sig.StrongLen)
			if bytes.Equal(d.sig.StrongSigs[blockIdx], strong2) {
				d.weakSum.Reset()
				d.block.Reset()
				err := d.match.add(MATCH_KIND_COPY, uint64(blockIdx)*uint64(d.sig.BlockLen), uint64(d.sig.BlockLen))
				if err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (d *DeltaStruct) End() error {
	for _, b := range d.block.Bytes() {
		err := d.match.add(MATCH_KIND_LITERAL, uint64(b), 1)
		if err != nil {
			return err
		}
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
