package js

import (
	"bytes"
	"syscall/js"

	"github.com/balena-os/librsync-go"
)

var delta *librsync.DeltaStruct

func NewDelta(this js.Value, args []js.Value) interface{} {
	sigMap := convertToObject(args[0])
	bufSize := args[1].Int()

	strongSigsRaw := sigMap["strongSigs"].([]interface{})

	strongSigs := make([][]byte, len(strongSigsRaw))
	for idx, x := range strongSigsRaw {
		strongSigs[idx] = x.([]byte)
	}

	weak2blockRaw := sigMap["weak2block"].(map[interface{}]interface{})

	weak2block := make(map[uint32]int)
	for k, v := range weak2blockRaw {
		weak2block[uint32(k.(int))] = v.(int)
	}

	signatureType := &librsync.SignatureType{
		SigType:    librsync.MagicNumber(sigMap["sigType"].(int)),
		BlockLen:   uint32(sigMap["blockLen"].(int)),
		StrongLen:  uint32(sigMap["strongLen"].(int)),
		StrongSigs: strongSigs,
		Weak2block: weak2block,
	}

	output := bytes.NewBuffer(nil)
	var err error
	delta, err = librsync.NewDelta(signatureType, output, bufSize)
	if err != nil {
		return []interface{}{nil, err.Error()}
	}

	obj := js.Global().Get("Object").New(nil)

	obj.Set("digest", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		uint8Array := args[0]
		data := byteSliceFromJS(uint8Array)

		err := delta.Digest(data)
		if err != nil {
			return []interface{}{nil, err.Error()}
		}

		b := output.Bytes()
		resultUint8Array := convertToJSBytes(b)
		output.Reset()
		return []interface{}{resultUint8Array, len(b), nil}
	}))

	obj.Set("end", js.FuncOf(func(this js.Value, args []js.Value) interface{} {
		err := delta.End()
		if err != nil {
			return []interface{}{nil, err}
		}

		b := output.Bytes()
		resultUint8Array := convertToJSBytes(b)
		output.Reset()
		return []interface{}{resultUint8Array, len(b), nil}
	}))

	return obj
}

func convertToJSBytes(b []byte) js.Value {
	var uint8Array js.Value
	uint8Array = js.Global().Get("Uint8Array").New(len(b))
	js.CopyBytesToJS(uint8Array, b)
	return uint8Array
}
