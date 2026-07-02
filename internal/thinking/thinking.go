// Package thinking is a minimal shim exposing the subset of the host's
// internal/thinking API that internal/runtime/executor/github_copilot_executor.go
// references. It exists so the executor body port can copy verbatim without
// touching import paths.
//
// ApplyThinking is intentionally a no-op because the host runs its own
// ThinkingApplier capability BEFORE dispatching to plugin executors — the
// payload arriving at Execute already has the thinking config applied by the
// host. Preserving the signature avoids surgery on the ported executor's
// call sites.
package thinking

import "strings"

// SuffixResult mirrors the host's thinking.SuffixResult shape.
type SuffixResult struct {
	// ModelName is the model identifier with the trailing "(suffix)" removed.
	ModelName string
	// HasSuffix reports whether a suffix was present.
	HasSuffix bool
	// RawSuffix is the raw text inside the parentheses when HasSuffix is true.
	RawSuffix string
}

// ParseSuffix is a verbatim port of internal/thinking/suffix.go:ParseSuffix
// from the host. It extracts "model-name(value)" without validating or
// interpreting the value — content interpretation belongs to Parse{Numeric,
// Level,Special}Suffix helpers that the plugin does not need.
func ParseSuffix(model string) SuffixResult {
	// Find the last opening parenthesis.
	lastOpen := strings.LastIndex(model, "(")
	if lastOpen == -1 {
		return SuffixResult{ModelName: model, HasSuffix: false}
	}

	// Require a closing parenthesis at the end.
	if !strings.HasSuffix(model, ")") {
		return SuffixResult{ModelName: model, HasSuffix: false}
	}

	return SuffixResult{
		ModelName: model[:lastOpen],
		HasSuffix: true,
		RawSuffix: model[lastOpen+1 : len(model)-1],
	}
}

// ApplyThinking is a no-op in the plugin. See package doc for rationale.
// The signature matches host thinking.ApplyThinking so the ported executor
// compiles without call-site changes; the body is discarded arguments and
// the payload flows through unchanged.
func ApplyThinking(body []byte, model, fromFormat, toFormat, providerKey string) ([]byte, error) {
	_ = model
	_ = fromFormat
	_ = toFormat
	_ = providerKey
	return body, nil
}
