package main

// Package-private helpers copied (or stubbed) from the host's
// internal/runtime/executor/* files. These symbols back the ported
// github_copilot_executor body without needing to change any of its call
// sites. Wrappers point at internal/helps for real logic; recording /
// reporting / config-override helpers are intentional no-ops because the
// host owns those layers (UsagePlugin, request-log, etc.).

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/conversun/cpa-plugin-copilot/internal/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// statusErr matches internal/runtime/executor/codex_websockets_executor.go:statusErr.
type statusErr struct {
	code int
	msg  string
}

func (e statusErr) Error() string   { return e.msg }
func (e statusErr) StatusCode() int { return e.code }

// dataTag is the SSE line prefix used by the executor's stream scanner.
var dataTag = []byte("data:")

// metaStringValue extracts a trimmed string from an auth Metadata map.
func metaStringValue(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	if v, ok := metadata[key]; ok {
		switch typed := v.(type) {
		case string:
			return strings.TrimSpace(typed)
		case []byte:
			return strings.TrimSpace(string(typed))
		}
	}
	return ""
}

// upstreamRequestLog mirrors helps.UpstreamRequestLog for the executor's
// recordAPIRequest call sites. In-plugin logging is dropped; the fields
// exist only so the executor body compiles unchanged.
type upstreamRequestLog struct {
	URL       string
	Method    string
	Headers   http.Header
	Body      []byte
	Provider  string
	AuthID    string
	AuthLabel string
	AuthType  string
	AuthValue string
}

// No-op recorders. The host owns request/response logging at a higher layer.
// Signatures use `any` for cfg to accept the executor's nil-passthrough after
// the internal/config drop.
func recordAPIRequest(_ context.Context, _ any, _ upstreamRequestLog)                        {}
func recordAPIResponseError(_ context.Context, _ any, _ error)                               {}
func recordAPIResponseMetadata(_ context.Context, _ any, _ int, _ http.Header)               {}
func appendAPIResponseChunk(_ context.Context, _ any, _ []byte)                              {}

// applyPayloadConfigWithRoot is an identity function in the plugin. Real
// host implementation reads user config for per-model overrides; plugin
// carries its own config path via `plugin.reconfigure`, out of scope here.
func applyPayloadConfigWithRoot(_ any, _ string, _ string, _ string, body []byte, _ []byte, _ string, _ string) []byte {
	return body
}

// newProxyAwareHTTPClient returns a default HTTP client. Real host version
// wires proxy config from cfg + auth-specific ProxyURL; upgrade path is
// pluginabi.MethodHostHTTPDo callback (tracked in README limitation #1).
// The 0-timeout preserves the executor's contract that no timeout is set
// once an upstream connection has been established (per AGENTS.md).
func newProxyAwareHTTPClient(_ context.Context, _ any, _ *cliproxyauth.Auth, _ time.Duration) *http.Client {
	// Route plugin HTTP through the host's transport policy (proxy config,
	// request-log capture, TLS profile). When stored_host is nil (test-time /
	// pre-init), hostHTTPTransport falls back to http.DefaultTransport so the
	// same client works without a host handshake.
	return &http.Client{Transport: newHostHTTPTransport()}
}

// payloadRequestedModel reads the originally-requested model name from
// opts.Metadata. Falls back to defaultModel when absent.
func payloadRequestedModel(opts cliproxyexecutor.Options, defaultModel string) string {
	if opts.Metadata != nil {
		if v, ok := opts.Metadata["requested_model"]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return defaultModel
}

// payloadRequestPath reads the request path from opts.Metadata (empty when
// absent — the host uses this to distinguish /chat/completions from /responses).
func payloadRequestPath(opts cliproxyexecutor.Options) string {
	if opts.Metadata != nil {
		if v, ok := opts.Metadata["request_path"]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}

// summarizeErrorBody produces a short readable summary of an upstream error
// body for log lines. Trims very long bodies to 500 bytes.
func summarizeErrorBody(_ string, body []byte) string {
	if len(body) > 500 {
		return string(body[:500]) + "..."
	}
	return string(body)
}

// parseOpenAI*Usage are thin wrappers around internal/helps. The body port
// preserves these names so executor call sites do not need surgery.
func parseOpenAIUsage(data []byte) helps.Detail                       { return helps.ParseOpenAIUsage(data) }
func parseOpenAIStreamUsage(line []byte) (helps.Detail, bool)         { return helps.ParseOpenAIStreamUsage(line) }
func parseOpenAIResponsesUsage(data []byte) helps.Detail              { return helps.ParseOpenAIUsage(data) }
func parseOpenAIResponsesStreamUsage(line []byte) (helps.Detail, bool) {
	return helps.ParseOpenAIStreamUsage(line)
}

// usageReporter is a stub for the host's helps.UsageReporter. The host
// records usage through its UsagePlugin capability; the plugin does not
// need to replicate that pipeline. Methods match what the executor calls.
type usageReporter struct{}

func newUsageReporter(_ context.Context, _ string, _ string, _ *cliproxyauth.Auth) *usageReporter {
	return &usageReporter{}
}

func (*usageReporter) trackFailure(_ context.Context, _ *error) {}
func (*usageReporter) publish(_ context.Context, _ helps.Detail) {}
func (*usageReporter) publishFailure(_ context.Context)          {}
func (*usageReporter) ensurePublished(_ context.Context)         {}
