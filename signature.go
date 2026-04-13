package librsync

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"golang.org/x/crypto/blake2b"
	"golang.org/x/crypto/md4"
)

const (
	BLAKE2_SUM_LENGTH = 32
	MD4_SUM_LENGTH    = 16
)

type SignatureType struct {
	SigType    MagicNumber
	BlockLen   uint32
	StrongLen  uint32
	StrongSigs [][]byte
	Weak2block map[uint32]int
}

type signature struct {
	*SignatureType
	maxStrongLen uint32
	block        []byte
	output       io.Writer
	strongBuf    []byte
	outBuf       [4 + BLAKE2_SUM_LENGTH]byte // pre-allocated buffer for combined weak+strong write
}

func NewSignature(sigType MagicNumber, blockLen, strongLen uint32, output io.Writer) (*signature, error) {
	var maxStrongLen uint32

	switch sigType {
	case BLAKE2_SIG_MAGIC:
		maxStrongLen = BLAKE2_SUM_LENGTH
	case MD4_SIG_MAGIC:
		maxStrongLen = MD4_SUM_LENGTH
	default:
		return nil, fmt.Errorf("invalid sigType %#x", sigType)
	}

	if strongLen > maxStrongLen {
		return nil, fmt.Errorf("invalid strongLen %d for sigType %#x", strongLen, sigType)
	}

	ret := &signature{
		SignatureType: &SignatureType{
			SigType:    sigType,
			BlockLen:   blockLen,
			StrongLen:  strongLen,
			Weak2block: make(map[uint32]int),
			StrongSigs: make([][]byte, 0),
		},
		maxStrongLen: maxStrongLen,
		block:        make([]byte, blockLen),
		output:       output,
		strongBuf:    make([]byte, maxStrongLen),
	}

	err := binary.Write(output, binary.BigEndian, sigType)
	if err != nil {
		return nil, err
	}
	err = binary.Write(output, binary.BigEndian, blockLen)
	if err != nil {
		return nil, err
	}
	err = binary.Write(output, binary.BigEndian, strongLen)
	if err != nil {
		return nil, err
	}

	return ret, nil
}

func (s *signature) Digest(b []byte) error {
	return s.DigestReader(bytes.NewReader(b))
}

func (s *signature) DigestReader(reader io.Reader) error {
	for {
		n, err := io.ReadAtLeast(reader, s.block, int(s.BlockLen))
		if err == io.EOF {
			// We reached the end of the input, we are done with the signature
			break
		} else if err == nil || err == io.ErrUnexpectedEOF {
			if n == 0 {
				// No real error and no new data either: that also signals the
				// end the input; we are done with the signature
				break
			}
			// No real error, got data. Leave this `if` and checksum this block
		} else if err != nil {
			// Got a real error, report it back to the caller
			return err
		}

		data := s.block[:n]

		weak := WeakChecksum(data)
		CalcStrongSumInto(s.strongBuf, data, s.SigType)

		binary.BigEndian.PutUint32(s.outBuf[:4], weak)
		copy(s.outBuf[4:], s.strongBuf[:s.StrongLen])
		if _, err := s.output.Write(s.outBuf[:4+s.StrongLen]); err != nil {
			return err
		}

		strong := make([]byte, s.StrongLen)
		copy(strong, s.strongBuf[:s.StrongLen])
		s.Weak2block[weak] = len(s.StrongSigs)
		s.StrongSigs = append(s.StrongSigs, strong)
	}
	return nil
}

func (s *signature) End() *SignatureType {
	return s.SignatureType
}

func CalcStrongSum(data []byte, sigType MagicNumber, strongLen uint32) ([]byte, error) {
	switch sigType {
	case BLAKE2_SIG_MAGIC:
		d := blake2b.Sum256(data)
		return d[:strongLen], nil
	case MD4_SIG_MAGIC:
		d := md4.New()
		d.Write(data)
		return d.Sum(nil)[:strongLen], nil
	}
	return nil, fmt.Errorf("invalid sigType %#x", sigType)
}

// CalcStrongSumInto writes the strong checksum into dst, which must be at
// least maxStrongLen bytes for the given sigType. It avoids the heap
// allocation that CalcStrongSum incurs from slicing a local array.
func CalcStrongSumInto(dst []byte, data []byte, sigType MagicNumber) {
	switch sigType {
	case BLAKE2_SIG_MAGIC:
		d := blake2b.Sum256(data)
		copy(dst, d[:])
	case MD4_SIG_MAGIC:
		d := md4.New()
		d.Write(data)
		copy(dst, d.Sum(nil))
	}
}

func Signature(input io.Reader, output io.Writer, blockLen, strongLen uint32, sigType MagicNumber) (*SignatureType, error) {
	sig, err := NewSignature(sigType, blockLen, strongLen, output)
	if err != nil {
		return nil, err
	}

	err = sig.DigestReader(input)
	if err != nil {
		return nil, err
	}
	return sig.End(), nil
}

// ReadSignature reads a signature from an io.Reader.
func ReadSignature(r io.Reader) (*SignatureType, error) {
	var magic MagicNumber
	err := binary.Read(r, binary.BigEndian, &magic)
	if err != nil {
		return nil, err
	}

	var blockLen uint32
	err = binary.Read(r, binary.BigEndian, &blockLen)
	if err != nil {
		return nil, err
	}

	var strongLen uint32
	err = binary.Read(r, binary.BigEndian, &strongLen)
	if err != nil {
		return nil, err
	}

	strongSigs := [][]byte{}
	weak2block := map[uint32]int{}

	entryBuf := make([]byte, 4+int(strongLen))
	for {
		_, err := io.ReadFull(r, entryBuf)
		if err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}

		weakSum := binary.BigEndian.Uint32(entryBuf[:4])
		strongSum := make([]byte, strongLen)
		copy(strongSum, entryBuf[4:])

		weak2block[weakSum] = len(strongSigs)
		strongSigs = append(strongSigs, strongSum)
	}

	return &SignatureType{
		SigType:    magic,
		BlockLen:   blockLen,
		StrongLen:  strongLen,
		StrongSigs: strongSigs,
		Weak2block: weak2block,
	}, nil
}

// ReadSignatureFile reads a signature from the file at path.
func ReadSignatureFile(path string) (*SignatureType, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ReadSignature(f)
}
