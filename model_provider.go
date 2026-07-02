package main

// model_provider.go implements the ModelProvider capability handlers:
//   - model.static  → returns the curated catalog from internal/models
//   - model.for_auth → returns the live /models list fetched via the
//                      auth's GitHub access token
//
// Both handlers wrap results in the pluginapi.ModelResponse shape.

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/conversun/cpa-plugin-copilot/copilot"
	"github.com/conversun/cpa-plugin-copilot/internal/models"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// handleModelStatic returns the static Copilot model catalog. Called when
// the host has no auth to consult (early startup, empty auth dir).
func handleModelStatic(_ context.Context, _ []byte) ([]byte, error) {
	return okEnvelope(pluginapi.ModelResponse{
		Provider: pluginIdentifier,
		Models:   models.StaticCatalog(),
	})
}

// handleModelForAuth fetches the live /models endpoint using the auth's
// GitHub access token, then adapts each entry into a pluginapi.ModelInfo.
// Falls back to the static catalog on any error so the user still gets a
// usable model picker even when the network / token is momentarily bad.
func handleModelForAuth(ctx context.Context, raw []byte) ([]byte, error) {
	var req pluginapi.AuthModelRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}

	accessToken := metaStringValue(req.Metadata, "access_token")
	if accessToken == "" {
		return okEnvelope(pluginapi.ModelResponse{
			Provider: pluginIdentifier,
			Models:   models.StaticCatalog(),
		})
	}

	authSvc := copilot.NewCopilotAuth(nil)
	entries, err := authSvc.ListModelsWithGitHubToken(ctx, accessToken)
	if err != nil || len(entries) == 0 {
		return okEnvelope(pluginapi.ModelResponse{
			Provider: pluginIdentifier,
			Models:   models.StaticCatalog(),
		})
	}

	converted := make([]pluginapi.ModelInfo, 0, len(entries))
	for _, entry := range entries {
		converted = append(converted, adaptCopilotModelEntry(entry))
	}
	return okEnvelope(pluginapi.ModelResponse{
		Provider: pluginIdentifier,
		Models:   converted,
	})
}

// adaptCopilotModelEntry converts a copilot.CopilotModelEntry (raw /models
// response) into a pluginapi.ModelInfo. Missing display names fall back to
// the ID, and token limits come from capabilities.limits when present.
func adaptCopilotModelEntry(entry copilot.CopilotModelEntry) pluginapi.ModelInfo {
	displayName := strings.TrimSpace(entry.Name)
	if displayName == "" {
		displayName = entry.ID
	}
	info := pluginapi.ModelInfo{
		ID:          entry.ID,
		Object:      "model",
		Created:     entry.Created,
		OwnedBy:     "github-copilot",
		Type:        "github-copilot",
		DisplayName: displayName,
		Version:     entry.Version,
	}
	if limits := entry.Limits(); limits != nil {
		info.ContextLength = int64(limits.MaxContextWindowTokens)
		info.InputTokenLimit = int64(limits.MaxPromptTokens)
		info.OutputTokenLimit = int64(limits.MaxOutputTokens)
		info.MaxCompletionTokens = int64(limits.MaxOutputTokens)
	}
	return info
}
