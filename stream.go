package main

// stream.go wires Executor.ExecuteStream onto the host's stream callback
// protocol. The plugin ABI cannot ship a Go channel over JSON RPC, so the
// wire contract is:
//
//   1. Host generates a stream_id and sends it in the execute_stream RPC
//      payload alongside the pluginapi.ExecutorRequest fields.
//   2. Plugin's handleExecutorExecuteStream unmarshals both, kicks off a
//      goroutine that drives the ported executor's real streaming path,
//      and returns an EMPTY ExecutorStreamResponse envelope right away.
//   3. The goroutine reads cliproxyexecutor.StreamChunk values from the
//      channel returned by executor.ExecuteStream. For each chunk it calls
//      hostCall("host.stream.emit", rpcStreamEmitRequest{StreamID, Payload}).
//   4. When the channel closes or an error occurs, the goroutine calls
//      hostCall("host.stream.close", rpcStreamCloseRequest{StreamID, Error}).
//
// This mirrors the pattern in examples/plugin/claude-web-search-router/go/
// stream_forward.go so the host handshake stays byte-identical to a known
// working reference plugin.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// rpcExecutorRequest is the wire shape for executor.execute and
// executor.execute_stream. It embeds the public pluginapi.ExecutorRequest
// and adds the two host-injected control fields the RPC layer uses to
// route stream chunks + nested host callbacks back to the right session.
type rpcExecutorRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

// rpcStreamEmitRequest matches examples/plugin/claude-web-search-router
// stream_forward.go so the host's stream.emit handler consumes an
// identical shape regardless of which plugin is emitting.
type rpcStreamEmitRequest struct {
	StreamID string `json:"stream_id"`
	Payload  []byte `json:"payload,omitempty"`
	Error    string `json:"error,omitempty"`
}

// rpcStreamCloseRequest carries the final stream state. Non-empty Error
// signals the host that the stream terminated abnormally.
type rpcStreamCloseRequest struct {
	StreamID string `json:"stream_id"`
	Error    string `json:"error,omitempty"`
}

// emitStreamChunk pushes one payload to the host under stream_id. Empty
// stream_id short-circuits so tests can hit this path without triggering
// an obvious host callback failure.
func emitStreamChunk(streamID string, payload []byte) error {
	if strings.TrimSpace(streamID) == "" {
		return errors.New("plugin stream id is required")
	}
	_, err := hostCallJSONReq(pluginabi.MethodHostStreamEmit, rpcStreamEmitRequest{
		StreamID: streamID,
		Payload:  payload,
	})
	return err
}

// closeStream signals stream termination to the host. Empty errMsg means
// clean EOF; any non-empty string surfaces at the host as a stream error.
func closeStream(streamID, errMsg string) {
	if strings.TrimSpace(streamID) == "" {
		return
	}
	_, _ = hostCallJSONReq(pluginabi.MethodHostStreamClose, rpcStreamCloseRequest{
		StreamID: streamID,
		Error:    strings.TrimSpace(errMsg),
	})
}

// hostCallJSONReq marshals request, dispatches through hostCall, and
// returns raw response bytes. Unlike hostCallJSON in host_call.go, this
// wrapper does not unmarshal a response: emit + close return an ack the
// stream loop does not consume.
func hostCallJSONReq(method string, request any) ([]byte, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	return hostCall(method, payload)
}

// runExecutorStream is the goroutine body: it drives the ported executor's
// ExecuteStream, pumps every chunk through emitStreamChunk, and always
// terminates with closeStream (success or failure).
//
// The function recovers from panics because a goroutine panic that
// escapes the plugin process would crash the host; a controlled
// closeStream("panic: ...") lets the host surface a proper error.
func runExecutorStream(ctx context.Context, req rpcExecutorRequest) {
	defer func() {
		if recovered := recover(); recovered != nil {
			closeStream(req.StreamID, fmt.Sprintf("stream orchestration panic: %v", recovered))
		}
	}()

	exec := getExecutor()
	auth := buildAuth(req.ExecutorRequest)
	execReq := buildExecRequest(req.ExecutorRequest)
	execOpts := buildExecOptions(req.ExecutorRequest)
	execOpts.Stream = true

	result, err := exec.ExecuteStream(ctx, auth, execReq, execOpts)
	if err != nil {
		closeStream(req.StreamID, err.Error())
		return
	}
	if result == nil || result.Chunks == nil {
		closeStream(req.StreamID, "executor returned nil StreamResult")
		return
	}

	for chunk := range result.Chunks {
		if chunk.Err != nil {
			closeStream(req.StreamID, chunk.Err.Error())
			return
		}
		if len(chunk.Payload) == 0 {
			continue
		}
		if errEmit := emitStreamChunk(req.StreamID, bytes.Clone(chunk.Payload)); errEmit != nil {
			// If the host callback itself fails (e.g., ErrHostNotAvailable
			// during a test-time dispatch), we still try to close cleanly
			// so the host state machine does not hang.
			closeStream(req.StreamID, "host stream emit failed: "+errEmit.Error())
			return
		}
	}
	closeStream(req.StreamID, "")
}
