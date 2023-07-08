package js

import (
	"syscall/js"

	"github.com/balena-os/librsync-go"
)

func Export() {
	librsyncObj := js.Global().Get("Object").New(nil)

	sigCls := js.FuncOf(NewSignature)
	librsyncObj.Set("Signature", sigCls)
	sigCls.Set("deserialize", js.FuncOf(Deserialize))

	librsyncObj.Set("Delta", js.FuncOf(NewDelta))

	librsyncObj.Set("BLAKE2_SIG_MAGIC", int(librsync.BLAKE2_SIG_MAGIC))
	librsyncObj.Set("MD4_SIG_MAGIC", int(librsync.MD4_SIG_MAGIC))
	js.Global().Set("librsync", librsyncObj)
}
