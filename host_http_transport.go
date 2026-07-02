package main

// host_http_transport.go wires plugin HTTP calls through the host's
// transport policy via hostCall("host.http.do", ...). Doing so means the
// host's proxy config + request-log capture + TLS profile all apply to
// plugin-originated calls, matching what a native host executor would get.
//
// The transport is used by newProxyAwareHTTPClient (see runtime_helpers.go).
// When stored_host has not been initialised (test-time, pre-init), the
// transport falls back to http.DefaultTransport so plugin code that runs
// before cliproxy_plugin_init still functions in unit tests.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// hostHTTPTransport implements http.RoundTripper by serialising each
// request over the host RPC and reconstituting the response. Streaming
// responses (Content-Type: text/event-stream) still work because the
// executor consumes the whole body upfront when it needs bytes — SSE
// forwarding paths use ExecuteStream, not this transport.
type hostHTTPTransport struct {
	// fallback is used before stored_host is set (unit tests). Real host
	// dispatches use hostCall regardless of this field.
	fallback http.RoundTripper
}

// newHostHTTPTransport constructs a transport that prefers host-routed
// requests but falls back to http.DefaultTransport when stored_host is nil.
func newHostHTTPTransport() *hostHTTPTransport {
	return &hostHTTPTransport{fallback: http.DefaultTransport}
}

// RoundTrip serialises the *http.Request, dispatches host.http.do, and
// reconstructs an *http.Response from the host's HTTPResponse envelope.
// The body reader on the outbound request is fully consumed (buffered),
// which matches host behaviour: the host's transport already does the same
// so plugin-side buffering does not change semantics.
func (t *hostHTTPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, errors.New("host http transport: request is nil")
	}

	var body []byte
	if req.Body != nil {
		buf, err := io.ReadAll(req.Body)
		if closeErr := req.Body.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
		if err != nil {
			return nil, err
		}
		body = buf
	}

	hostReq := pluginapi.HTTPRequest{
		Method:  req.Method,
		URL:     req.URL.String(),
		Headers: cloneHeader(req.Header),
		Body:    body,
	}

	respBytes, err := hostCallJSONReq(pluginabi.MethodHostHTTPDo, hostReq)
	if err != nil {
		if errors.Is(err, ErrHostNotAvailable) && t.fallback != nil {
			// Test-time / pre-init path: rebuild the body reader so the
			// fallback transport sees the same request the host would have.
			req.Body = io.NopCloser(bytes.NewReader(body))
			return t.fallback.RoundTrip(req)
		}
		return nil, err
	}

	return decodeHostHTTPResponse(req, respBytes)
}

// decodeHostHTTPResponse pulls a pluginapi.HTTPResponse out of the
// {ok, result, error} envelope the host returns and materialises it as a
// standard *http.Response the caller can consume. Error envelopes surface
// as a Go error so callers see the failure at Do() rather than inside the
// body.
func decodeHostHTTPResponse(req *http.Request, raw []byte) (*http.Response, error) {
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, err
	}
	if !env.OK {
		msg := "host.http.do returned error envelope"
		if env.Error != nil {
			msg = "host.http.do: " + env.Error.Code + ": " + env.Error.Message
		}
		return nil, errors.New(msg)
	}
	var hostResp pluginapi.HTTPResponse
	if err := json.Unmarshal(env.Result, &hostResp); err != nil {
		return nil, err
	}

	status := hostResp.StatusCode
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		Status:        strings.TrimSpace(http.StatusText(status)),
		StatusCode:    status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        hostResp.Headers,
		Body:          io.NopCloser(bytes.NewReader(hostResp.Body)),
		ContentLength: int64(len(hostResp.Body)),
		Request:       req,
	}, nil
}

// cloneHeader is a nil-safe http.Header.Clone shim; some paths hand us
// nil Header maps (e.g. tests constructing requests directly).
func cloneHeader(h http.Header) http.Header {
	if h == nil {
		return http.Header{}
	}
	return h.Clone()
}

// hostRoutedContext is a no-op marker so future code that wants to
// verify a request went through the host transport can check
// context values without imposing global state. Not currently used but
// left in place so wiring downstream (e.g., usage plugin metadata) does
// not require another transport swap.
type hostRoutedContext struct{}

// contextWithHostRouted is retained as a placeholder for callers that
// eventually want to tag host-routed contexts.
func contextWithHostRouted(ctx context.Context) context.Context {
	return context.WithValue(ctx, hostRoutedContext{}, true)
}

// hostRoutedFromContext reports whether a context was tagged as
// host-routed. Currently only exercised in tests.
func hostRoutedFromContext(ctx context.Context) bool {
	v, _ := ctx.Value(hostRoutedContext{}).(bool)
	return v
}
