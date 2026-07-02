// Package registry is a minimal shim providing the subset of the host's
// internal/registry API that github_copilot_executor.go references.
//
// The plugin does not maintain a live model registry — the ported executor's
// runtime lookups (GetGlobalRegistry().GetModelInfo, GetStaticModelDefinitionsByChannel)
// will hit stubs that return nil, causing the executor to fall through to
// its static-heuristic path (strings.Contains(model, "codex") for the
// Responses-endpoint decision, etc.). When the ModelProvider capability
// lands, real static model data will be exposed via that capability
// instead of being replicated in this shim.
package registry

// ModelInfo mirrors the host's registry.ModelInfo shape, kept wide so that
// executor code accessing any field compiles unchanged. Only ID and
// SupportedEndpoints are actively read by the executor today; the other
// fields exist to match FetchGitHubCopilotModels' construction pattern in
// case that function is ever pulled into the plugin.
type ModelInfo struct {
	ID                         string
	Object                     string
	Created                    int64
	OwnedBy                    string
	Type                       string
	DisplayName                string
	Name                       string
	Version                    string
	Description                string
	InputTokenLimit            int
	OutputTokenLimit           int
	SupportedGenerationMethods []string
	ContextLength              int64
	MaxCompletionTokens        int64
	SupportedEndpoints         []string
}

// ModelRegistry is a stub. All lookups return nil so callers fall through
// to their static-list fallback paths.
type ModelRegistry struct{}

// GetGlobalRegistry returns an empty stub registry. The host's real
// implementation is a sync.Once-backed singleton with cross-provider model
// bookkeeping; nothing of that is useful inside an isolated plugin process.
func GetGlobalRegistry() *ModelRegistry {
	return &ModelRegistry{}
}

// GetModelInfo always returns nil in the shim. Executor call sites already
// handle nil by falling through to lookupGitHubCopilotStaticModelInfo (which
// itself falls through to GetStaticModelDefinitionsByChannel below).
func (r *ModelRegistry) GetModelInfo(modelID, provider string) *ModelInfo {
	_ = modelID
	_ = provider
	return nil
}

// GetStaticModelDefinitionsByChannel returns nil until the ModelProvider
// capability lands. lookupGitHubCopilotStaticModelInfo (in the ported
// executor) already has the correct final fallback: the
// `strings.Contains(baseModel, "codex")` heuristic for the Responses
// endpoint switch.
func GetStaticModelDefinitionsByChannel(channel string) []*ModelInfo {
	_ = channel
	return nil
}

// GetGitHubCopilotModels returns nil. This function is only called from
// FetchGitHubCopilotModels (host-side, sdk/cliproxy/service.go); the
// executor body port drops FetchGitHubCopilotModels because
// ModelProvider.ModelsForAuth replaces it.
func GetGitHubCopilotModels() []*ModelInfo {
	return nil
}
