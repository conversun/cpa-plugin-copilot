package main

// host_call.go is the plugin → host RPC wrapper (pure Go). The C plumbing
// (call_host_api / free_host_buffer / stored_host) lives in main.go's cgo
// preamble; invokeHostAPI is the sole entry-point that speaks C. Keeping
// the cgo blocks in a single file avoids duplicate-symbol linker errors
// when multiple Go files each import "C" and reference the same helpers.
//
// Use cases (both wired later — infrastructure landed here):
//   - host.stream.emit / host.stream.close — for Executor.ExecuteStream to
//     push chunks back as they arrive from the ported executor.
//   - host.http.do — future migration of plugin HTTP calls onto the host's
//     transport policy (proxy config + request-log capture).
//
// Every entry point is safe to call before stored_host is set (pre-init
// path); callers see ErrHostNotAvailable and can decide how to degrade.

import (
	"encoding/json"
	"errors"
)

// ErrHostNotAvailable is returned when hostCall runs before
// cliproxy_plugin_init has stored the host callback pointer. Callers that
// might legitimately hit this path (e.g., unit tests dispatching methods
// directly) should either check for this sentinel or design flows so the
// call only happens after plugin.register has completed.
var ErrHostNotAvailable = errors.New("host callback not available (stored_host is nil)")

// hostCall dispatches an RPC method into the host using JSON payload
// semantics. Response bytes are copied into a Go slice inside invokeHostAPI,
// so callers can retain them beyond the underlying C buffer's lifetime.
//
// Return contract:
//   - err == ErrHostNotAvailable : stored_host was nil at the time of the
//     call. Common in unit tests; in production this only happens if a
//     handler runs before cliproxy_plugin_init has completed.
//   - err == nil                 : call succeeded; out holds the response.
//   - other err                  : host returned a non-zero status code.
func hostCall(method string, payload []byte) ([]byte, error) {
	rc, out := invokeHostAPI(method, payload)
	switch rc {
	case 0:
		return out, nil
	case 1:
		return nil, ErrHostNotAvailable
	default:
		return nil, errors.New("host call returned non-zero status")
	}
}

// hostCallJSON marshals request, calls the host, and unmarshals the response.
// A convenience for the common case where both sides speak JSON envelopes.
// Not currently used by any production path but exists so future callers
// (streaming, HTTP passthrough) do not have to repeat the marshal dance.
func hostCallJSON(method string, request any, out any) error {
	payload, err := json.Marshal(request)
	if err != nil {
		return err
	}
	respBytes, err := hostCall(method, payload)
	if err != nil {
		return err
	}
	if out == nil || len(respBytes) == 0 {
		return nil
	}
	return json.Unmarshal(respBytes, out)
}
