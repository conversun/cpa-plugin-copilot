package main

// adapter.go bridges the pluginapi.ExecutorRequest / .ExecutorHTTPRequest
// wire types onto the sdk/cliproxy/auth.Auth + sdk/cliproxy/executor.Request /
// Options types that the ported GitHubCopilotExecutor was built for.
//
// The plugin process holds ONE GitHubCopilotExecutor singleton (via sync.Once)
// so its Copilot-API-token cache survives across multiple invocations.

import (
	"sync"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var (
	sharedExecutorOnce sync.Once
	sharedExecutor     *GitHubCopilotExecutor
)

// getExecutor returns the shared GitHubCopilotExecutor. The Copilot API
// token cache lives on that instance; sharing it across calls avoids
// re-exchanging tokens on every request.
func getExecutor() *GitHubCopilotExecutor {
	sharedExecutorOnce.Do(func() {
		sharedExecutor = NewGitHubCopilotExecutor()
	})
	return sharedExecutor
}

// authFromRequest reconstructs a *cliproxyauth.Auth from the flat fields the
// host serialises into pluginapi.ExecutorRequest / .ExecutorHTTPRequest.
// The Storage field is intentionally left nil: the executor only reads
// auth.Metadata (for access_token) and auth.Attributes; Storage is used by
// higher layers that never call into a plugin executor.
func authFromRequest(id, provider, label string, metadata map[string]any, attributes map[string]string) *cliproxyauth.Auth {
	auth := &cliproxyauth.Auth{
		ID:         id,
		Provider:   provider,
		Label:      label,
		Metadata:   metadata,
		Attributes: attributes,
	}
	if auth.Metadata == nil {
		auth.Metadata = map[string]any{}
	}
	if auth.Attributes == nil {
		auth.Attributes = map[string]string{}
	}
	return auth
}

// buildAuth is a thin wrapper that pulls auth fields from a
// pluginapi.ExecutorRequest.
func buildAuth(req pluginapi.ExecutorRequest) *cliproxyauth.Auth {
	return authFromRequest(req.AuthID, req.AuthProvider, req.AuthID, req.AuthMetadata, req.AuthAttributes)
}

// buildExecRequest maps pluginapi.ExecutorRequest onto cliproxyexecutor.Request.
func buildExecRequest(req pluginapi.ExecutorRequest) cliproxyexecutor.Request {
	return cliproxyexecutor.Request{
		Model:    req.Model,
		Payload:  req.Payload,
		Format:   sdktranslator.FromString(req.Format),
		Metadata: req.Metadata,
	}
}

// buildExecOptions maps pluginapi.ExecutorRequest onto cliproxyexecutor.Options.
func buildExecOptions(req pluginapi.ExecutorRequest) cliproxyexecutor.Options {
	return cliproxyexecutor.Options{
		Stream:          req.Stream,
		Alt:             req.Alt,
		Headers:         req.Headers,
		Query:           req.Query,
		OriginalRequest: req.OriginalRequest,
		SourceFormat:    sdktranslator.FromString(req.SourceFormat),
		Metadata:        req.Metadata,
	}
}
