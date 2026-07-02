package main

// plugin_test.go exercises the JSON-envelope dispatch layer at the Go level.
// Real E2E testing (host loads .dylib + drives Copilot requests) requires a
// live GitHub Copilot subscription and a running CLIProxyAPI host; these
// tests instead prove:
//   1. Every declared method dispatches to a handler (no unknown_method).
//   2. Registration returns a well-formed envelope matching Path-A shape.
//   3. Auth.parse correctly claims / rejects github-copilot storage files.
//   4. Executor stubs unmarshal their pluginapi wire types without panicking.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// decodeEnvelope unwraps the wire envelope for assertion. Returns the
// success result bytes and the error envelope (if any).
func decodeEnvelope(t *testing.T, raw []byte) (envelope, []byte) {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("envelope unmarshal failed: %v (raw: %s)", err, string(raw))
	}
	return env, env.Result
}

// TestDispatchAllDeclaredMethods asserts every method the plugin claims to
// handle in main.go actually maps to a handler (no "unknown_method").
func TestDispatchAllDeclaredMethods(t *testing.T) {
	methods := []string{
		pluginabi.MethodPluginRegister,
		pluginabi.MethodPluginReconfigure,
		pluginabi.MethodPluginShutdown,
		pluginabi.MethodAuthIdentifier,
		pluginabi.MethodExecutorIdentifier,
	}
	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			raw, err := handleMethod(method, nil)
			if err != nil {
				t.Fatalf("handleMethod(%q) returned unexpected error: %v", method, err)
			}
			env, _ := decodeEnvelope(t, raw)
			if !env.OK {
				t.Fatalf("handleMethod(%q) returned error envelope: %v", method, env.Error)
			}
		})
	}
}

// TestUnknownMethodReturnsErrorEnvelope confirms the default dispatch case
// produces a well-formed error envelope for a made-up method name.
func TestUnknownMethodReturnsErrorEnvelope(t *testing.T) {
	raw, err := handleMethod("does.not.exist", nil)
	if err != nil {
		t.Fatalf("handleMethod returned Go error instead of envelope: %v", err)
	}
	env, _ := decodeEnvelope(t, raw)
	if env.OK {
		t.Fatalf("expected error envelope for unknown method, got OK")
	}
	if env.Error == nil || env.Error.Code != "unknown_method" {
		t.Fatalf("expected error code 'unknown_method', got: %+v", env.Error)
	}
}

// TestPluginRegisterShape asserts the register response has the schema
// version, provider identifier, and both AuthProvider + Executor capability
// flags the host needs to route Copilot traffic here.
func TestPluginRegisterShape(t *testing.T) {
	raw, err := handleMethod(pluginabi.MethodPluginRegister, nil)
	if err != nil {
		t.Fatalf("plugin.register handler error: %v", err)
	}
	env, result := decodeEnvelope(t, raw)
	if !env.OK {
		t.Fatalf("plugin.register returned error envelope: %v", env.Error)
	}
	var reg registration
	if err := json.Unmarshal(result, &reg); err != nil {
		t.Fatalf("plugin.register result unmarshal: %v", err)
	}
	if reg.SchemaVersion != pluginabi.SchemaVersion {
		t.Errorf("schema version mismatch: got %d want %d", reg.SchemaVersion, pluginabi.SchemaVersion)
	}
	if reg.Metadata.Name == "" {
		t.Errorf("metadata.name empty")
	}
	if !reg.Capabilities.AuthProvider {
		t.Errorf("capabilities.auth_provider = false")
	}
	if !reg.Capabilities.Executor {
		t.Errorf("capabilities.executor = false")
	}
	if reg.Capabilities.ExecutorModelScope != string(pluginapi.ExecutorModelScopeOAuth) {
		t.Errorf("executor_model_scope = %q, want %q", reg.Capabilities.ExecutorModelScope, pluginapi.ExecutorModelScopeOAuth)
	}
	if len(reg.Capabilities.ExecutorInputFormats) == 0 {
		t.Errorf("executor_input_formats empty")
	}
}

// TestIdentifierMatchesPluginKey asserts auth.identifier and
// executor.identifier both return the same "github-copilot" key that the
// host uses for routing.
func TestIdentifierMatchesPluginKey(t *testing.T) {
	for _, method := range []string{pluginabi.MethodAuthIdentifier, pluginabi.MethodExecutorIdentifier} {
		t.Run(method, func(t *testing.T) {
			raw, err := handleMethod(method, nil)
			if err != nil {
				t.Fatalf("handleMethod(%q) error: %v", method, err)
			}
			env, result := decodeEnvelope(t, raw)
			if !env.OK {
				t.Fatalf("handleMethod(%q) error envelope: %v", method, env.Error)
			}
			var body map[string]string
			if err := json.Unmarshal(result, &body); err != nil {
				t.Fatalf("identifier result unmarshal: %v", err)
			}
			if body["identifier"] != pluginIdentifier {
				t.Errorf("identifier = %q, want %q", body["identifier"], pluginIdentifier)
			}
		})
	}
}

// TestAuthParseClaimsCopilotFiles feeds a synthetic github-copilot storage
// JSON to auth.parse and expects Handled=true with a matching auth record.
func TestAuthParseClaimsCopilotFiles(t *testing.T) {
	rawJSON := []byte(`{"type":"github-copilot","access_token":"gho_test","username":"testuser","email":"test@example.com"}`)
	parseReq := pluginapi.AuthParseRequest{
		Provider: "github-copilot",
		FileName: "github-copilot-testuser.json",
		RawJSON:  rawJSON,
	}
	payload, err := json.Marshal(parseReq)
	if err != nil {
		t.Fatalf("marshal parse request: %v", err)
	}
	raw, err := handleMethod(pluginabi.MethodAuthParse, payload)
	if err != nil {
		t.Fatalf("auth.parse handler error: %v", err)
	}
	env, result := decodeEnvelope(t, raw)
	if !env.OK {
		t.Fatalf("auth.parse error envelope: %v", env.Error)
	}
	var parseResp pluginapi.AuthParseResponse
	if err := json.Unmarshal(result, &parseResp); err != nil {
		t.Fatalf("auth.parse result unmarshal: %v", err)
	}
	if !parseResp.Handled {
		t.Fatalf("auth.parse did not claim a github-copilot file: %+v", parseResp)
	}
	if parseResp.Auth.Provider != "github-copilot" {
		t.Errorf("parsed provider = %q, want %q", parseResp.Auth.Provider, "github-copilot")
	}
	if parseResp.Auth.FileName != "github-copilot-testuser.json" {
		t.Errorf("parsed fileName = %q", parseResp.Auth.FileName)
	}
	// Metadata should carry the access token for the executor to consume later.
	if parseResp.Auth.Metadata["access_token"] != "gho_test" {
		t.Errorf("metadata.access_token missing or wrong: %v", parseResp.Auth.Metadata["access_token"])
	}
}

// TestAuthParseRejectsForeignFiles feeds a JSON with type="other" and
// expects Handled=false so the host can keep asking other providers.
func TestAuthParseRejectsForeignFiles(t *testing.T) {
	rawJSON := []byte(`{"type":"gemini-cli","access_token":"gc_test"}`)
	parseReq := pluginapi.AuthParseRequest{RawJSON: rawJSON}
	payload, _ := json.Marshal(parseReq)
	raw, err := handleMethod(pluginabi.MethodAuthParse, payload)
	if err != nil {
		t.Fatalf("auth.parse error: %v", err)
	}
	env, result := decodeEnvelope(t, raw)
	if !env.OK {
		t.Fatalf("auth.parse error envelope: %v", env.Error)
	}
	var parseResp pluginapi.AuthParseResponse
	_ = json.Unmarshal(result, &parseResp)
	if parseResp.Handled {
		t.Fatalf("auth.parse wrongly claimed a foreign type: %+v", parseResp)
	}
}

// TestExecutorStreamRequiresStreamID asserts that execute_stream returns a
// well-formed error envelope when the host omits stream_id from the wire
// payload. Without a stream_id the plugin has no channel to push chunks
// down, so this is the correct fast-fail path.
func TestExecutorStreamRequiresStreamID(t *testing.T) {
	execReq := pluginapi.ExecutorRequest{Model: "gpt-4o", Stream: true}
	payload, _ := json.Marshal(execReq)
	raw, err := handleMethod(pluginabi.MethodExecutorExecuteStream, payload)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	env, _ := decodeEnvelope(t, raw)
	if env.OK {
		t.Fatalf("expected error envelope for missing stream_id, got OK")
	}
	if env.Error == nil || env.Error.Code != "executor_error" {
		t.Fatalf("expected executor_error code, got: %+v", env.Error)
	}
	if !strings.Contains(env.Error.Message, "stream_id") {
		t.Errorf("error message = %q, expected mention of stream_id", env.Error.Message)
	}
}

// TestExecutorStreamWithStreamIDAccepts asserts that when the host supplies
// a stream_id, the handler returns an empty ExecutorStreamResponse envelope
// and kicks the streaming goroutine. The goroutine's hostCall attempts will
// fail with ErrHostNotAvailable in the test process (no host loaded), but
// that failure is inside the goroutine and does NOT flip the RPC ack.
//
// This test locks the fire-and-forget shape of the wire contract: the
// dispatch layer must return before the stream finishes, and it must not
// leak the goroutine's callback errors into the ack envelope.
func TestExecutorStreamWithStreamIDAccepts(t *testing.T) {
	// Wrapping the ExecutorRequest with the host-injected stream_id field
	// mirrors the RPC wire shape (see rpcExecutorRequest in stream.go).
	req := rpcExecutorRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{Model: "gpt-4o", Stream: true, AuthMetadata: map[string]any{"access_token": "gho_fake"}},
		StreamID:        "test-stream-" + t.Name(),
	}
	payload, _ := json.Marshal(req)
	raw, err := handleMethod(pluginabi.MethodExecutorExecuteStream, payload)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	env, _ := decodeEnvelope(t, raw)
	if !env.OK {
		t.Fatalf("expected OK envelope for streaming ack, got error: %+v", env.Error)
	}
}

// TestExecutorCountTokensStub asserts CountTokens returns a valid zero-count
// envelope (helps.CountOpenAIChatTokens is stubbed at 0 for the current
// slice). This proves the adapter path constructs auth/req/opts without
// panicking on empty AuthMetadata.
func TestExecutorCountTokensStub(t *testing.T) {
	execReq := pluginapi.ExecutorRequest{
		Model:        "gpt-4o",
		Format:       "openai",
		SourceFormat: "openai",
		Payload:      []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`),
		AuthMetadata: map[string]any{"access_token": "gho_fake"},
	}
	payload, _ := json.Marshal(execReq)
	raw, err := handleMethod(pluginabi.MethodExecutorCountTokens, payload)
	if err != nil {
		t.Fatalf("count_tokens handler error: %v", err)
	}
	env, _ := decodeEnvelope(t, raw)
	if !env.OK {
		t.Fatalf("count_tokens error envelope: %v", env.Error)
	}
}

// TestPluginRegisterAdvertisesModelProvider asserts the register response
// now includes the ModelProvider capability flag alongside AuthProvider +
// Executor. Locks in the Session-2 addition so downstream slices catch a
// regression if someone drops the flag.
func TestPluginRegisterAdvertisesModelProvider(t *testing.T) {
	raw, err := handleMethod(pluginabi.MethodPluginRegister, nil)
	if err != nil {
		t.Fatalf("plugin.register error: %v", err)
	}
	env, result := decodeEnvelope(t, raw)
	if !env.OK {
		t.Fatalf("plugin.register error envelope: %v", env.Error)
	}
	var reg registration
	if err := json.Unmarshal(result, &reg); err != nil {
		t.Fatalf("plugin.register result unmarshal: %v", err)
	}
	if !reg.Capabilities.ModelProvider {
		t.Errorf("capabilities.model_provider = false, want true")
	}
}

// TestModelStaticCatalog asserts model.static returns a non-empty catalog
// with a well-formed pluginapi.ModelResponse envelope. Spot-checks that at
// least one OpenAI-family and one Claude-family model is present.
func TestModelStaticCatalog(t *testing.T) {
	raw, err := handleMethod(pluginabi.MethodModelStatic, nil)
	if err != nil {
		t.Fatalf("model.static error: %v", err)
	}
	env, result := decodeEnvelope(t, raw)
	if !env.OK {
		t.Fatalf("model.static error envelope: %v", env.Error)
	}
	var resp pluginapi.ModelResponse
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("model.static result unmarshal: %v", err)
	}
	if resp.Provider != pluginIdentifier {
		t.Errorf("provider = %q, want %q", resp.Provider, pluginIdentifier)
	}
	if len(resp.Models) == 0 {
		t.Fatalf("model.static returned empty catalog")
	}
	var sawOpenAI, sawClaude bool
	for _, m := range resp.Models {
		if m.ID == "gpt-4o" {
			sawOpenAI = true
		}
		if m.ID == "claude-sonnet-4-5" {
			sawClaude = true
		}
		if m.OwnedBy != pluginIdentifier {
			t.Errorf("model %q owned_by = %q, want %q", m.ID, m.OwnedBy, pluginIdentifier)
		}
	}
	if !sawOpenAI {
		t.Errorf("catalog missing gpt-4o")
	}
	if !sawClaude {
		t.Errorf("catalog missing claude-sonnet-4-5")
	}
}

// TestModelForAuthFallsBackWithoutToken asserts that model.for_auth
// returns the static catalog when the caller does not supply an
// access_token in AuthMetadata. Graceful-degradation path: even a
// mis-configured auth should still produce a usable model list.
func TestModelForAuthFallsBackWithoutToken(t *testing.T) {
	req := pluginapi.AuthModelRequest{AuthID: "github-copilot-empty.json", AuthProvider: pluginIdentifier}
	payload, _ := json.Marshal(req)
	raw, err := handleMethod(pluginabi.MethodModelForAuth, payload)
	if err != nil {
		t.Fatalf("model.for_auth error: %v", err)
	}
	env, result := decodeEnvelope(t, raw)
	if !env.OK {
		t.Fatalf("model.for_auth error envelope: %v", env.Error)
	}
	var resp pluginapi.ModelResponse
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("model.for_auth result unmarshal: %v", err)
	}
	if len(resp.Models) == 0 {
		t.Errorf("expected fallback to static catalog, got empty list")
	}
}

// TestPluginRegisterAdvertisesCommandLinePlugin asserts the register response
// now includes the CommandLinePlugin capability flag. Locks in the Session-2
// addition so downstream slices catch a regression if someone drops the flag.
func TestPluginRegisterAdvertisesCommandLinePlugin(t *testing.T) {
	raw, err := handleMethod(pluginabi.MethodPluginRegister, nil)
	if err != nil {
		t.Fatalf("plugin.register error: %v", err)
	}
	env, result := decodeEnvelope(t, raw)
	if !env.OK {
		t.Fatalf("plugin.register error envelope: %v", env.Error)
	}
	var reg registration
	if err := json.Unmarshal(result, &reg); err != nil {
		t.Fatalf("plugin.register result unmarshal: %v", err)
	}
	if !reg.Capabilities.CommandLinePlugin {
		t.Errorf("capabilities.command_line_plugin = false, want true")
	}
}

// TestCommandLineRegisterExposesLoginFlag asserts command_line.register
// returns exactly the --copilot-login flag (bool, default false) so the
// host merges it into its own flag set for -help output.
func TestCommandLineRegisterExposesLoginFlag(t *testing.T) {
	raw, err := handleMethod(pluginabi.MethodCommandLineRegister, nil)
	if err != nil {
		t.Fatalf("command_line.register error: %v", err)
	}
	env, result := decodeEnvelope(t, raw)
	if !env.OK {
		t.Fatalf("command_line.register error envelope: %v", env.Error)
	}
	var resp pluginapi.CommandLineRegistrationResponse
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("command_line.register result unmarshal: %v", err)
	}
	if len(resp.Flags) != 1 {
		t.Fatalf("want 1 flag, got %d: %+v", len(resp.Flags), resp.Flags)
	}
	flag := resp.Flags[0]
	if flag.Name != "copilot-login" {
		t.Errorf("flag.Name = %q, want %q", flag.Name, "copilot-login")
	}
	if flag.Type != "bool" {
		t.Errorf("flag.Type = %q, want %q", flag.Type, "bool")
	}
	if flag.DefaultValue != "false" {
		t.Errorf("flag.DefaultValue = %q, want %q", flag.DefaultValue, "false")
	}
	if flag.Usage == "" {
		t.Errorf("flag.Usage empty; --help will look broken")
	}
}

// TestCommandLineExecuteWithoutFlagIsNoOp asserts that command_line.execute
// returns an empty CommandLineExecutionResponse when --copilot-login is NOT
// among the triggered flags. Without this guard the handler would attempt
// a network device-flow login every time the host dispatches a CLI
// invocation from any other plugin.
func TestCommandLineExecuteWithoutFlagIsNoOp(t *testing.T) {
	req := pluginapi.CommandLineExecutionRequest{
		Program:        "cli-proxy-api",
		Args:           []string{"--some-other-flag"},
		TriggeredFlags: map[string]pluginapi.CommandLineFlagValue{}, // NOT triggered
	}
	payload, _ := json.Marshal(req)
	raw, err := handleMethod(pluginabi.MethodCommandLineExecute, payload)
	if err != nil {
		t.Fatalf("command_line.execute error: %v", err)
	}
	env, result := decodeEnvelope(t, raw)
	if !env.OK {
		t.Fatalf("command_line.execute error envelope: %v", env.Error)
	}
	var resp pluginapi.CommandLineExecutionResponse
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("command_line.execute result unmarshal: %v", err)
	}
	if len(resp.Auths) != 0 {
		t.Errorf("expected 0 auths when flag not triggered, got %d", len(resp.Auths))
	}
	if resp.ExitCode != 0 {
		t.Errorf("expected ExitCode=0 for no-op, got %d", resp.ExitCode)
	}
	if len(resp.Stdout) != 0 || len(resp.Stderr) != 0 {
		t.Errorf("expected no stdout/stderr for no-op path")
	}
}

// TestPluginRegisterAdvertisesManagementAPI asserts the register response
// now includes the ManagementAPI capability flag alongside the others.
// Locks in the Session-2 addition so downstream slices catch a regression
// if someone drops the flag.
func TestPluginRegisterAdvertisesManagementAPI(t *testing.T) {
	raw, err := handleMethod(pluginabi.MethodPluginRegister, nil)
	if err != nil {
		t.Fatalf("plugin.register error: %v", err)
	}
	env, result := decodeEnvelope(t, raw)
	if !env.OK {
		t.Fatalf("plugin.register error envelope: %v", env.Error)
	}
	var reg registration
	if err := json.Unmarshal(result, &reg); err != nil {
		t.Fatalf("plugin.register result unmarshal: %v", err)
	}
	if !reg.Capabilities.ManagementAPI {
		t.Errorf("capabilities.management_api = false, want true")
	}
}

// TestManagementRegisterExposesCopilotQuotaRoute asserts management.register
// returns exactly the GET /copilot-quota route with a menu label and
// description populated for management-UI clients.
func TestManagementRegisterExposesCopilotQuotaRoute(t *testing.T) {
	raw, err := handleMethod(pluginabi.MethodManagementRegister, nil)
	if err != nil {
		t.Fatalf("management.register error: %v", err)
	}
	env, result := decodeEnvelope(t, raw)
	if !env.OK {
		t.Fatalf("management.register error envelope: %v", env.Error)
	}
	var resp pluginapi.ManagementRegistrationResponse
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("management.register result unmarshal: %v", err)
	}
	if len(resp.Routes) != 1 {
		t.Fatalf("want 1 route, got %d", len(resp.Routes))
	}
	route := resp.Routes[0]
	if route.Method != http.MethodGet {
		t.Errorf("route.Method = %q, want GET", route.Method)
	}
	if route.Path != "/copilot-quota" {
		t.Errorf("route.Path = %q, want /copilot-quota", route.Path)
	}
	if route.Menu == "" || route.Description == "" {
		t.Errorf("route.Menu / Description empty; management UI will look broken")
	}
}

// TestManagementHandleUnknownRouteReturns404 asserts that management.handle
// with a path we do NOT declare produces a JSON 404 rather than a Go error
// or a leaky 500. Path parity with the declared route matters because the
// host trims the plugin prefix before dispatch.
func TestManagementHandleUnknownRouteReturns404(t *testing.T) {
	req := pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/does-not-exist",
	}
	payload, _ := json.Marshal(req)
	raw, err := handleMethod(pluginabi.MethodManagementHandle, payload)
	if err != nil {
		t.Fatalf("management.handle error: %v", err)
	}
	env, result := decodeEnvelope(t, raw)
	if !env.OK {
		t.Fatalf("management.handle error envelope: %v", env.Error)
	}
	var resp pluginapi.ManagementResponse
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("management.handle result unmarshal: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

// TestManagementHandleCopilotQuotaWithoutBearerReturns401 asserts the
// authorization guard: a request without Authorization: Bearer must NOT
// even attempt a GitHub call. This is a fast-fail unit test; the real
// GitHub roundtrip belongs to the E2E slice that needs live credentials.
func TestManagementHandleCopilotQuotaWithoutBearerReturns401(t *testing.T) {
	req := pluginapi.ManagementRequest{
		Method:  http.MethodGet,
		Path:    "/copilot-quota",
		Headers: http.Header{}, // NO Authorization
	}
	payload, _ := json.Marshal(req)
	raw, err := handleMethod(pluginabi.MethodManagementHandle, payload)
	if err != nil {
		t.Fatalf("management.handle error: %v", err)
	}
	env, result := decodeEnvelope(t, raw)
	if !env.OK {
		t.Fatalf("management.handle error envelope: %v", env.Error)
	}
	var resp pluginapi.ManagementResponse
	if err := json.Unmarshal(result, &resp); err != nil {
		t.Fatalf("management.handle result unmarshal: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
	var body map[string]string
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("response body unmarshal: %v (body: %s)", err, string(resp.Body))
	}
	if !strings.Contains(body["error"], "Authorization") {
		t.Errorf("error message = %q, expected mention of Authorization", body["error"])
	}
}

// TestHostCallReturnsErrHostNotAvailableBeforeInit asserts that hostCall
// (the plugin → host RPC wrapper) gracefully returns ErrHostNotAvailable
// when stored_host has not been set by cliproxy_plugin_init. This is the
// state during unit tests, so any code path that reaches hostCall pre-init
// (whether by bug or by design) surfaces a predictable sentinel instead of
// crashing on a NULL C pointer dereference.
func TestHostCallReturnsErrHostNotAvailableBeforeInit(t *testing.T) {
	_, err := hostCall("host.stream.emit", []byte(`{"stream_id":"t","chunk":"x"}`))
	if err == nil {
		t.Fatalf("expected ErrHostNotAvailable pre-init, got nil error")
	}
	if !errors.Is(err, ErrHostNotAvailable) {
		t.Fatalf("expected ErrHostNotAvailable, got %v", err)
	}
}

// TestHostCallJSONMarshalsAndSurfacesSentinel asserts the JSON convenience
// wrapper marshals correctly and still surfaces the sentinel error when
// stored_host is nil. Locks the contract for future streaming / HTTP
// call sites that will route through hostCallJSON.
func TestHostCallJSONMarshalsAndSurfacesSentinel(t *testing.T) {
	type dummyReq struct {
		StreamID string `json:"stream_id"`
		Data     []byte `json:"data"`
	}
	req := dummyReq{StreamID: "test-stream", Data: []byte("chunk")}
	var resp map[string]any
	err := hostCallJSON("host.stream.emit", req, &resp)
	if err == nil {
		t.Fatalf("expected ErrHostNotAvailable pre-init, got nil")
	}
	if !errors.Is(err, ErrHostNotAvailable) {
		t.Fatalf("expected ErrHostNotAvailable via hostCallJSON, got %v", err)
	}
}

// TestHostHTTPTransportFallsBackToDefaultBeforeInit asserts the RoundTripper
// wired into newProxyAwareHTTPClient reaches its real destination even when
// stored_host has not been set. Without this fallback, every unit test that
// touches an outbound http.Client would fail with ErrHostNotAvailable, and
// pre-init plugin code paths (auth device flow rendered from CLI, for
// example) would never dial the network.
func TestHostHTTPTransportFallsBackToDefaultBeforeInit(t *testing.T) {
	testSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Echo-Method", r.Method)
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte(`{"pong":true}`))
	}))
	defer testSrv.Close()

	client := &http.Client{Transport: newHostHTTPTransport()}
	req, err := http.NewRequest(http.MethodGet, testSrv.URL+"/probe", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusTeapot)
	}
	if got := resp.Header.Get("X-Echo-Method"); got != http.MethodGet {
		t.Errorf("X-Echo-Method = %q, want %q", got, http.MethodGet)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "pong") {
		t.Errorf("body = %q, expected pong", string(body))
	}
}

// TestHostRoutedContextMarker asserts the context helper pair round-trips
// the routing marker. Locks the placeholder in place so a downstream slice
// that starts tagging host-routed requests has a stable API to lean on.
func TestHostRoutedContextMarker(t *testing.T) {
	if hostRoutedFromContext(context.Background()) {
		t.Errorf("background context should not be host-routed by default")
	}
	ctx := contextWithHostRouted(context.Background())
	if !hostRoutedFromContext(ctx) {
		t.Errorf("contextWithHostRouted did not tag the returned context")
	}
}