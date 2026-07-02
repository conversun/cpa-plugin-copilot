// Package models publishes the GitHub Copilot model catalog the plugin
// hands back to the host via ModelProvider.StaticModels and .ModelsForAuth.
//
// The static list is a curated subset of the host's
// internal/registry/model_definitions.go:GetGitHubCopilotModels() (32
// models). For a live/authoritative catalog, ModelsForAuth pulls
// /models directly through copilot.ListModelsWithGitHubToken and adapts
// each entry into a pluginapi.ModelInfo.
package models

import (
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	provider = "github-copilot"
	// createdEpoch matches the host's synthetic creation timestamp
	// (2024-11-27) so downstream clients see identical "created" values
	// whether we serve the plugin catalog or the host's static one.
	createdEpoch = int64(1732752000)
	// copilotChatPath / copilotResponsesPath / copilotMessagesPath are the
	// upstream endpoint identifiers the executor uses to pick between
	// OpenAI Chat Completions, OpenAI Responses, and Anthropic gateway
	// (the Anthropic path was dropped this cut — see plugin roadmap).
	copilotChatPath      = "/chat/completions"
	copilotResponsesPath = "/responses"
	copilotMessagesPath  = "/messages"
)

// openaiEndpoints covers OpenAI-family Copilot models (Chat Completions +
// Responses API for GPT-4.1 / GPT-5 / o-series reasoning).
var openaiEndpoints = []string{copilotChatPath, copilotResponsesPath}

// claudeEndpoints covers Anthropic-family Copilot models (Chat Completions +
// Anthropic Messages via Copilot gateway). Claude models remain listed for
// user discoverability even though nativeGateway is not wired in this cut.
var claudeEndpoints = []string{copilotChatPath, copilotMessagesPath}

// geminiEndpoints covers Gemini-family Copilot models.
var geminiEndpoints = []string{copilotChatPath}

// StaticCatalog returns the curated list of Copilot models the plugin
// advertises when the host has no live auth to query. This is a subset
// intended to cover the models most users actually pick; ModelsForAuth
// fills the remainder from Copilot's live /models endpoint.
func StaticCatalog() []pluginapi.ModelInfo {
	return []pluginapi.ModelInfo{
		// OpenAI Chat Completions + Responses API models
		newOpenAI("gpt-4.1", "GPT-4.1", 128000, 16384),
		newOpenAI("gpt-4o", "GPT-4o", 128000, 16384),
		newOpenAI("gpt-4o-mini", "GPT-4o mini", 128000, 16384),
		newOpenAI("o1", "OpenAI o1", 200000, 100000),
		newOpenAI("o1-mini", "OpenAI o1 mini", 128000, 65536),
		newOpenAI("o3-mini", "OpenAI o3 mini", 200000, 100000),
		// Anthropic-family via Copilot (require nativeGateway which is
		// deferred; listed so users can see them in model pickers).
		newClaude("claude-sonnet-4-5", "Claude Sonnet 4.5", 200000, 8192),
		newClaude("claude-opus-4", "Claude Opus 4", 200000, 8192),
		newClaude("claude-3-5-sonnet", "Claude 3.5 Sonnet", 200000, 8192),
		// Gemini-family via Copilot
		newGemini("gemini-2.5-pro", "Gemini 2.5 Pro", 1048576, 65536),
		newGemini("gemini-2.0-flash", "Gemini 2.0 Flash", 1048576, 8192),
	}
}

// newOpenAI constructs a ModelInfo entry for an OpenAI-family Copilot model.
// Owned-by / Type / Object mirror the host's static-list conventions so a
// client cannot distinguish plugin-served entries from host-served ones.
func newOpenAI(id, displayName string, contextLen, maxCompletion int64) pluginapi.ModelInfo {
	return pluginapi.ModelInfo{
		ID:                  id,
		Object:              "model",
		Created:             createdEpoch,
		OwnedBy:             provider,
		Type:                provider,
		DisplayName:         displayName,
		Description:         "OpenAI " + displayName + " via GitHub Copilot",
		ContextLength:       contextLen,
		MaxCompletionTokens: maxCompletion,
	}
}

// newClaude constructs a ModelInfo entry for an Anthropic-family Copilot
// model. See StaticCatalog rationale for listing these while nativeGateway
// is still deferred.
func newClaude(id, displayName string, contextLen, maxCompletion int64) pluginapi.ModelInfo {
	return pluginapi.ModelInfo{
		ID:                  id,
		Object:              "model",
		Created:             createdEpoch,
		OwnedBy:             provider,
		Type:                provider,
		DisplayName:         displayName,
		Description:         "Anthropic " + displayName + " via GitHub Copilot",
		ContextLength:       contextLen,
		MaxCompletionTokens: maxCompletion,
	}
}

// newGemini constructs a ModelInfo entry for a Gemini-family Copilot model.
func newGemini(id, displayName string, contextLen, maxCompletion int64) pluginapi.ModelInfo {
	return pluginapi.ModelInfo{
		ID:                  id,
		Object:              "model",
		Created:             createdEpoch,
		OwnedBy:             provider,
		Type:                provider,
		DisplayName:         displayName,
		Description:         "Google " + displayName + " via GitHub Copilot",
		ContextLength:       contextLen,
		MaxCompletionTokens: maxCompletion,
	}
}
