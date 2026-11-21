package main

// #include "abi.h"
import "C"

import (
	"encoding/json"
	"fmt"
	"unsafe"

	"github.com/moraxyc/cpa-reductant/internal/adapter"
	"github.com/moraxyc/cpa-reductant/internal/engine"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

type cgoHostCaller struct{}

func (cgoHostCaller) Call(method string, request, reply any) error {
	payload, err := json.Marshal(request)
	if err != nil {
		return err
	}
	name := C.CString(method)
	defer C.free(unsafe.Pointer(name))
	input := C.CBytes(payload)
	defer C.free(input)

	var out C.rpc_buffer
	status := C.call_host(name, (*C.uint8_t)(input), C.size_t(len(payload)), &out)
	defer C.release_host(out)

	var envelope pluginabi.Envelope
	if err := json.Unmarshal(C.GoBytes(out.ptr, C.int(out.len)), &envelope); err != nil {
		return fmt.Errorf("decode %s: %w", method, err)
	}
	if envelope.Error != nil {
		return envelope.Error
	}
	if status != 0 || !envelope.OK {
		return fmt.Errorf("%s failed (ABI status %d)", method, status)
	}
	if reply != nil {
		return json.Unmarshal(envelope.Result, reply)
	}
	return nil
}

var plugin = adapter.NewPlugin(engine.New(nil), cgoHostCaller{})

func main() {}

//export plugin_shutdown
func plugin_shutdown() {
	plugin.Shutdown()
}

//export plugin_call
func plugin_call(method *C.char, input *C.uint8_t, length C.size_t, out *C.rpc_buffer) C.int {
	*out = C.rpc_buffer{}
	result, err := plugin.Dispatch(C.GoString(method), C.GoBytes(unsafe.Pointer(input), C.int(length)))
	var reply []byte
	if err == nil {
		var payload []byte
		payload, err = json.Marshal(result)
		if err == nil {
			reply, err = json.Marshal(pluginabi.Envelope{OK: true, Result: payload})
		}
	}
	if err != nil {
		failure, ok := err.(*pluginabi.Error)
		if !ok {
			failure = pluginabi.NewError("reductant_error", err.Error())
		}
		reply, _ = json.Marshal(pluginabi.Envelope{Error: failure})
	}
	out.ptr, out.len = C.CBytes(reply), C.size_t(len(reply))
	return 0
}
