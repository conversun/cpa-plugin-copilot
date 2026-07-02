// Package main is the C-shared entry point for the GitHub Copilot provider plugin.
// It wires the C ABI required by CLIProxyAPI pluginhost to the plain-Go
// AuthProvider adapter in auth_provider.go and the registration in register.go.
//
// The C skeleton is deliberately identical to the one shipped in
// examples/plugin/*/go/main.go so future host ABI bumps only need to touch a
// single upstream reference point.
package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {
	stored_host = host;
}

// call_host_api invokes stored_host->call on behalf of Go code that needs
// to make a callback into the host (host.stream.emit, host.http.do, etc.).
// Returns non-zero when stored_host has not been initialised.
static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) {
		return 1;
	}
	return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}

// free_host_buffer frees a buffer the host allocated for a callback response.
// Go code holds C.CBytes'd copies of any bytes it wants to keep past the call.
static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) {
		stored_host->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"context"
	"encoding/json"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	C.store_host_api(host)
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, errHandle := handleMethod(C.GoString(method), requestBytes)
	if errHandle != nil {
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {}

// handleMethod dispatches a single plugin RPC call to the appropriate handler.
// Unknown methods return a well-formed error envelope rather than a hard
// failure so the host can log-and-continue if this plugin is ever wired to a
// capability method it hasn't opted into.
func handleMethod(method string, request []byte) ([]byte, error) {
	ctx := context.Background()
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodPluginShutdown:
		return okEnvelope(struct{}{})
	case pluginabi.MethodAuthIdentifier:
		return okEnvelope(map[string]string{"identifier": pluginIdentifier})
	case pluginabi.MethodAuthParse:
		return handleAuthParse(ctx, request)
	case pluginabi.MethodAuthLoginStart:
		return handleAuthLoginStart(ctx, request)
	case pluginabi.MethodAuthLoginPoll:
		return handleAuthLoginPoll(ctx, request)
	case pluginabi.MethodAuthRefresh:
		return handleAuthRefresh(ctx, request)
	case pluginabi.MethodModelStatic:
		return handleModelStatic(ctx, request)
	case pluginabi.MethodModelForAuth:
		return handleModelForAuth(ctx, request)
	case pluginabi.MethodExecutorIdentifier:
		return handleExecutorIdentifier(ctx, request)
	case pluginabi.MethodExecutorExecute:
		return handleExecutorExecute(ctx, request)
	case pluginabi.MethodExecutorExecuteStream:
		return handleExecutorExecuteStream(ctx, request)
	case pluginabi.MethodExecutorCountTokens:
		return handleExecutorCountTokens(ctx, request)
	case pluginabi.MethodExecutorHTTPRequest:
		return handleExecutorHTTPRequest(ctx, request)
	case pluginabi.MethodCommandLineRegister:
		return handleCommandLineRegister(ctx, request)
	case pluginabi.MethodCommandLineExecute:
		return handleCommandLineExecute(ctx, request)
	case pluginabi.MethodManagementRegister:
		return handleManagementRegister(ctx, request)
	case pluginabi.MethodManagementHandle:
		return handleManagementHandle(ctx, request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

// okEnvelope marshals v as the "result" field of a success envelope.
// v is expected to already be JSON-encodable; any error is surfaced to the
// caller so the ABI layer can report it as a plugin_error to the host.
func okEnvelope(v any) ([]byte, error) {
	result, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: result})
}

// errorEnvelope constructs a JSON error envelope. Marshal failure is
// deliberately swallowed because the envelope contents are static and cannot
// realistically fail; if it ever does, we return the empty byte slice and the
// ABI layer treats that as an unknown plugin failure.
func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

// writeResponse copies the plugin's response into a host-owned buffer allocated
// via C.CBytes. The host is responsible for calling cliproxyPluginFree once it
// has consumed the response.
func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}

// invokeHostAPI is the ONE bridge from Go into the C `call_host_api`
// helper. Keeping this cgo interaction in main.go (the only file with the
// static C definitions) means other Go files can drive host callbacks via
// this pure-Go entry point without dragging their own cgo preamble in and
// tripping duplicate-symbol linker errors.
//
// Semantics:
//   rc == 1 : stored_host has not been initialised (or its call fn is nil).
//             Callers that legitimately hit this pre-handshake path should
//             translate to a well-known sentinel error.
//   rc == 0 : success; response bytes are copied out of the host-owned
//             buffer before it is freed so the returned slice is safe to
//             retain past the call.
//   other  : generic host error; response buffer (if any) is freed.
func invokeHostAPI(method string, payload []byte) (rc int, out []byte) {
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))

	var req *C.uint8_t
	if len(payload) > 0 {
		req = (*C.uint8_t)(C.CBytes(payload))
		defer C.free(unsafe.Pointer(req))
	}

	var response C.cliproxy_buffer
	status := C.call_host_api(cMethod, req, C.size_t(len(payload)), &response)
	rc = int(status)
	if response.ptr != nil {
		if rc == 0 && response.len > 0 {
			out = C.GoBytes(response.ptr, C.int(response.len))
		}
		C.free_host_buffer(response.ptr, response.len)
	}
	return rc, out
}
