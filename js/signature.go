package js

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"syscall/js"

	"github.com/balena-os/librsync-go"
)

type SignatureType struct {
	*librsync.SignatureType
}

func (sig *SignatureType) toJSObject() js.Value {
	obj := js.Global().Get("Object").New(nil)
	obj.Set("sigType", int(sig.SigType))
	obj.Set("blockLen", int(sig.BlockLen))
	obj.Set("strongLen", int(sig.StrongLen))
	strongSigs := js.Global().Get("Array").New(len(sig.StrongSigs))
	Uint8Array := js.Global().Get("Uint8Array")
	for idx, b := range sig.StrongSigs {
		uint8Array := Uint8Array.New(len(b))
		js.CopyBytesToJS(uint8Array, b)
		strongSigs.SetIndex(idx, uint8Array)
	}
	obj.Set("strongSigs", strongSigs)
	weak2block := js.Global().Get("Map").New(nil)
	for k, v := range sig.Weak2block {
		weak2block.Call("set", int(k), v)
	}
	obj.Set("weak2block", weak2block)
	return obj
}

func NewSignature(this js.Value, args []js.Value) interface{} {
	blockLen := uint32(args[0].Int())
	strongLen := uint32(args[1].Int())
	sigType := librsync.MagicNumber(args[2].Int())

	sig, err := librsync.NewSignature(sigType, blockLen, strongLen, io.Discard)
	if err != nil {
		js.Global().Get("console").Call("error", fmt.Sprintf("Error creating new signature: %v", err))
		return nil
	}
	obj := js.Global().Get("Object").New(nil)

	obj.Set("digest", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		uint8Array := args[0]
		data := byteSliceFromJS(uint8Array)
		if err := sig.Digest(data); err != nil {
			return err.Error()
		}
		return nil
	}))

	obj.Set("end", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		sigType := sig.End()
		s := SignatureType{sigType}
		return s.toJSObject()
	}))

	obj.Set("serialize", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		buf := bytes.NewBuffer(nil)
		binary.Write(buf, binary.BigEndian, sig.SigType)
		binary.Write(buf, binary.BigEndian, blockLen)
		binary.Write(buf, binary.BigEndian, strongLen)
		for k, v := range sig.Weak2block {
			binary.Write(buf, binary.BigEndian, k)
			strong := sig.StrongSigs[v]
			binary.Write(buf, binary.BigEndian, strong)
		}
		b := buf.Bytes()
		uint8Array := js.Global().Get("Uint8Array").New(len(b))
		js.CopyBytesToJS(uint8Array, b)
		return uint8Array
	}))
	return obj
}

func Deserialize(this js.Value, args []js.Value) interface{} {
	uint8Array := args[0]
	b := byteSliceFromJS(uint8Array)
	buf := bytes.NewBuffer(b)
	sig, err := librsync.ReadSignature(buf)
	if err != nil {
		return []interface{}{nil, err.Error()}
	}
	s := &SignatureType{
		SignatureType: sig,
	}
	return []interface{}{s.toJSObject(), nil}
}

func byteSliceFromJS(uint8Array js.Value) []byte {
	byteSlice := make([]byte, uint8Array.Length())
	js.CopyBytesToGo(byteSlice, uint8Array)
	return byteSlice
}
