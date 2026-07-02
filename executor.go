package main

// executor.go is the pluginapi-facing dispatch for the plugin's executor
// capability. Each handler unmarshals its wire request, builds
// cliproxyauth.Auth + cliproxyexecutor.Request / Options via adapter.go, then
// invokes the shared ported GitHubCopilotExecutor. Non-streaming Execute,
// CountTokens, and HttpRequest are fully wired. ExecuteStream still returns
// `not_implemented`: the plugin ABI expects the host to consume a channel of
// ExecutorStreamChunk, which requires an RPC path (host.stream.emit or a
// stream_id + host.model.stream_read poll) that is not wired in this cut.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// handleExecutorIdentifier answers the identifier lookup the host performs
// during executor registration. Must match pluginIdentifier so the host
// routes Copilot models to this plugin.
func handleExecutorIdentifier(_ context.Context, _ []byte) ([]byte, error) {
	return okEnvelope(map[string]string{"identifier": pluginIdentifier})
}

// handleExecutorExecute drives non-streaming Chat Completions / Responses.
// The path: unmarshal → construct Auth/Request/Options → invoke the ported
// executor → repack Response. Errors surface through the executor's own
// statusErr type; the host translates non-200 codes downstream.
func handleExecutorExecute(ctx context.Context, raw []byte) ([]byte, error) {
	var req pluginapi.ExecutorRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}

	exec := getExecutor()
	auth := buildAuth(req)
	execReq := buildExecRequest(req)
	execOpts := buildExecOptions(req)

	resp, err := exec.Execute(ctx, auth, execReq, execOpts)
	if err != nil {
		return errorEnvelope("executor_error", err.Error()), nil
	}

	return okEnvelope(pluginapi.ExecutorResponse{
		Payload:  resp.Payload,
		Headers:  resp.Headers,
		Metadata: resp.Metadata,
	})
}

// handleExecutorExecuteStream drives streaming via the host's callback
// protocol. The plugin ABI cannot ship a Go channel over JSON RPC, so we
// return an empty ExecutorStreamResponse envelope immediately and spawn a
// goroutine that reads chunks from the ported executor's StreamResult and
// pushes each one through hostCall("host.stream.emit", ...). Termination
// (clean EOF or error) is signalled with hostCall("host.stream.close", ...).
//
// See stream.go for the runExecutorStream body; see
// examples/plugin/claude-web-search-router/go/stream_forward.go for the
// reference pattern this handler mirrors.
func handleExecutorExecuteStream(ctx context.Context, raw []byte) ([]byte, error) {
	var req rpcExecutorRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	if req.StreamID == "" {
		return errorEnvelope("executor_error", "stream_id is required for executor.execute_stream"), nil
	}
	// Streaming is fire-and-forget from the RPC layer's perspective: the
	// goroutine keeps running past this handler's return, using hostCall
	// to push chunks. context.Background() shields the goroutine from
	// this handler's ctx cancellation on RPC completion.
	go runExecutorStream(context.Background(), req)
	// ExecutorStreamResponse cannot be JSON-marshalled (its Chunks field is a
	// <-chan). The ack is intentionally an empty object — the host owns the
	// stream state machine now, and future chunks arrive via stream.emit.
	return okEnvelope(struct{}{})
}

// handleExecutorCountTokens delegates to the ported executor's CountTokens.
// The real implementation uses tiktoken (helps.CountOpenAIChatTokens); the
// current helps stub returns 0, so this handler will report 0 tokens until
// the helps port lands. That is intentional: token-first flows should not
// fail before the tokenizer is real.
func handleExecutorCountTokens(ctx context.Context, raw []byte) ([]byte, error) {
	var req pluginapi.ExecutorRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}

	exec := getExecutor()
	auth := buildAuth(req)
	execReq := buildExecRequest(req)
	execOpts := buildExecOptions(req)

	resp, err := exec.CountTokens(ctx, auth, execReq, execOpts)
	if err != nil {
		return errorEnvelope("executor_error", err.Error()), nil
	}
	return okEnvelope(pluginapi.ExecutorResponse{
		Payload:  resp.Payload,
		Headers:  resp.Headers,
		Metadata: resp.Metadata,
	})
}

// handleExecutorHTTPRequest builds a plain *http.Request from the wire
// payload, dispatches through the ported executor (which injects Copilot
// auth headers), and repacks the response. The response body is fully read
// into memory — the plugin ABI does not stream through this method.
func handleExecutorHTTPRequest(ctx context.Context, raw []byte) ([]byte, error) {
	var req pluginapi.ExecutorHTTPRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.URL, bytes.NewReader(req.Body))
	if err != nil {
		return errorEnvelope("bad_request", err.Error()), nil
	}
	for name, values := range req.Headers {
		for _, v := range values {
			httpReq.Header.Add(name, v)
		}
	}

	exec := getExecutor()
	auth := authFromRequest(req.AuthID, req.AuthProvider, req.AuthID, req.Metadata, req.Attributes)

	httpResp, err := exec.HttpRequest(ctx, auth, httpReq)
	if err != nil {
		return errorEnvelope("executor_error", err.Error()), nil
	}
	defer func() { _ = httpResp.Body.Close() }()

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return errorEnvelope("read_error", err.Error()), nil
	}

	return okEnvelope(pluginapi.ExecutorHTTPResponse{
		StatusCode: httpResp.StatusCode,
		Headers:    httpResp.Header,
		Body:       body,
	})
}
