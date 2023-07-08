package js

import "syscall/js"

func convertToGoType(dataValue js.Value) interface{} {
	dataType := dataValue.Type()
	var data interface{}
	switch dataType {
	case js.TypeBoolean:
		data = dataValue.Bool()
	case js.TypeNumber:
		data = dataValue.Int()
	case js.TypeObject:
		data = convertJSObjectToGoType(dataValue)
	case js.TypeString:
		data = dataValue.String()
	case js.TypeNull:
		data = nil
	case js.TypeUndefined:
		data = nil
	case js.TypeSymbol:
		data = dataValue.String()
	}
	return data
}

func convertJSObjectToGoType(value js.Value) interface{} {
	if value.InstanceOf(js.Global().Get("Uint8Array")) {
		return convertToBytes(value)
	} else if js.Global().Get("Array").Get("isArray").Invoke(value).Bool() {
		return convertToArray(value)
	} else if value.InstanceOf(js.Global().Get("Map")) {
		return convertMap(value)
	}
	return convertToObject(value)
}

func convertToBytes(value js.Value) []byte {
	length := value.Length()
	bytes := make([]byte, length)
	js.CopyBytesToGo(bytes, value)
	return bytes
}

func convertToArray(value js.Value) []interface{} {
	length := value.Length()
	array := make([]interface{}, length)
	for i := 0; i < length; i++ {
		array[i] = convertToGoType(value.Index(i))
	}
	return array
}

func convertToObject(value js.Value) map[string]interface{} {
	object := make(map[string]interface{})
	keys := js.Global().Get("Object").Call("keys", value)
	length := keys.Length()
	for i := 0; i < length; i++ {
		key := keys.Index(i).String()
		object[key] = convertToGoType(value.Get(key))
	}
	return object
}

func convertMap(jsMap js.Value) map[interface{}]interface{} {
	object := make(map[interface{}]interface{})
	keys := js.Global().Get("Array").Call("from", jsMap.Call("keys"))
	length := keys.Length()
	for i := 0; i < length; i++ {
		key := convertToGoType(keys.Index(i))
		object[key] = convertToGoType(jsMap.Call("get", key))
	}
	return object
}
