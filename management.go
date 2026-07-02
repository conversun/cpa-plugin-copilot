package main

// management.go wires the ManagementAPI capability. The plugin declares a
// single GET route at `/copilot-quota` that mirrors the host's
// /v0/management/copilot-quota endpoint (see api_tools.go:GetCopilotQuota).
// The response body is exactly the JSON GitHub returns from
// /copilot_internal/user, so downstream tooling built against the host
// endpoint continues to work.
//
// Auth sourcing: this cut requires the caller to pass the GitHub access
// token via `Authorization: Bearer <token>` header. Once the C ABI host
// callback plumbing lands (pluginabi.MethodHostAuthList/Get), the handler
// will resolve the token from the host's auth pool by `auth_index` query
// parameter, matching host parity.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	// copilotQuotaRoute is the plugin-owned Management API path. Host
	// exposes plugin routes under /v0/management/plugins/<pluginID>/, so
	// the final URL becomes /v0/management/plugins/github-copilot/copilot-quota.
	copilotQuotaRoute = "/copilot-quota"

	// copilotQuotaEndpoint is GitHub's Copilot quota API. Returns detailed
	// quota_snapshots for chat/completions/premium_interactions.
	copilotQuotaEndpoint = "https://api.github.com/copilot_internal/user"

	// copilotQuotaTimeout bounds the GitHub call. Long enough for a slow
	// GitHub API day, short enough that a stuck TCP does not hang the host.
	copilotQuotaTimeout = 15 * time.Second

	// bearerPrefix is the Authorization scheme the handler expects.
	bearerPrefix = "Bearer "
)

// handleManagementRegister answers management.register by declaring the
// GET /copilot-quota route. Menu + Description exist for management-UI
// clients that render plugin routes in a nav tree.
func handleManagementRegister(_ context.Context, _ []byte) ([]byte, error) {
	return okEnvelope(pluginapi.ManagementRegistrationResponse{
		Routes: []pluginapi.ManagementRoute{
			{
				Method:      http.MethodGet,
				Path:        copilotQuotaRoute,
				Menu:        "Copilot Quota",
				Description: "GitHub Copilot quota snapshot (chat, completions, premium_interactions).",
			},
		},
	})
}

// handleManagementHandle dispatches an incoming ManagementRequest to the
// plugin's route handlers. Currently only GET /copilot-quota is declared;
// anything else returns 404.
func handleManagementHandle(ctx context.Context, raw []byte) ([]byte, error) {
	var req pluginapi.ManagementRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}

	// Route table. Adding more plugin management routes = one more case.
	switch {
	case req.Method == http.MethodGet && strings.EqualFold(strings.TrimRight(req.Path, "/"), copilotQuotaRoute):
		return okEnvelope(handleCopilotQuota(ctx, req))
	default:
		return okEnvelope(managementNotFound(req.Method, req.Path))
	}
}

// handleCopilotQuota extracts a GitHub access token from the incoming
// Authorization header, calls GitHub's /copilot_internal/user endpoint
// with the Copilot client identity (matches
// internal/auth/copilot/copilot_auth.go), and forwards the JSON response
// verbatim so callers see the same shape as the host endpoint.
func handleCopilotQuota(ctx context.Context, req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	token, err := extractBearerToken(req.Headers)
	if err != nil {
		return jsonErrorResponse(http.StatusUnauthorized, err.Error())
	}

	upstream, err := buildCopilotQuotaRequest(ctx, token)
	if err != nil {
		return jsonErrorResponse(http.StatusInternalServerError, "failed to build upstream request: "+err.Error())
	}

	client := &http.Client{Timeout: copilotQuotaTimeout}
	resp, err := client.Do(upstream)
	if err != nil {
		return jsonErrorResponse(http.StatusBadGateway, "github copilot quota fetch failed: "+err.Error())
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			// Body close errors on GET responses are non-fatal but should
			// still be visible in logs when they happen.
			_ = errClose
		}
	}()

	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return jsonErrorResponse(http.StatusBadGateway, "reading github copilot quota response failed: "+readErr.Error())
	}

	// Forward the upstream status + JSON body verbatim so clients built
	// against the host endpoint see identical bytes on the wire.
	headers := http.Header{}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		headers.Set("Content-Type", ct)
	} else {
		headers.Set("Content-Type", "application/json")
	}
	return pluginapi.ManagementResponse{
		StatusCode: resp.StatusCode,
		Headers:    headers,
		Body:       body,
	}
}

// extractBearerToken pulls the token from an `Authorization: Bearer <token>`
// header. Non-Bearer schemes and missing headers surface as errors so the
// caller sees a clear 401 rather than a mysterious upstream 401.
func extractBearerToken(headers http.Header) (string, error) {
	auth := strings.TrimSpace(headers.Get("Authorization"))
	if auth == "" {
		return "", errors.New("missing Authorization: Bearer <github-access-token> header")
	}
	if !strings.HasPrefix(auth, bearerPrefix) {
		return "", errors.New("Authorization header must use Bearer scheme")
	}
	token := strings.TrimSpace(strings.TrimPrefix(auth, bearerPrefix))
	if token == "" {
		return "", errors.New("Bearer token is empty")
	}
	return token, nil
}

// buildCopilotQuotaRequest constructs the outbound GitHub request with the
// full Copilot client identity. Header values match
// internal/auth/copilot/copilot_auth.go so the endpoint accepts us under
// the May 10, 2026 integration-id requirement.
func buildCopilotQuotaRequest(ctx context.Context, token string) (*http.Request, error) {
	upstream, err := http.NewRequestWithContext(ctx, http.MethodGet, copilotQuotaEndpoint, nil)
	if err != nil {
		return nil, err
	}
	upstream.Header.Set("Authorization", "Bearer "+token)
	upstream.Header.Set("Accept", "application/json")
	upstream.Header.Set("User-Agent", "GitHubCopilotChat/0.50.0")
	upstream.Header.Set("Editor-Version", "vscode/1.122.0")
	upstream.Header.Set("Editor-Plugin-Version", "copilot-chat/0.50.0")
	upstream.Header.Set("Copilot-Integration-Id", "vscode-chat")
	upstream.Header.Set("X-Github-Api-Version", "2025-04-01")
	return upstream, nil
}

// managementNotFound produces a JSON 404 for unknown routes so the client
// sees a consistent error shape whether the miss is a wrong method or an
// undeclared path.
func managementNotFound(method, path string) pluginapi.ManagementResponse {
	return jsonErrorResponse(http.StatusNotFound, "unknown route: "+method+" "+path)
}

// jsonErrorResponse builds a small `{"error": "..."}` body with the given
// status. All plugin-side management errors flow through this so tests can
// assert on both status and shape.
func jsonErrorResponse(status int, message string) pluginapi.ManagementResponse {
	body, _ := json.Marshal(map[string]string{"error": message})
	return pluginapi.ManagementResponse{
		StatusCode: status,
		Headers:    http.Header{"Content-Type": []string{"application/json"}},
		Body:       body,
	}
}
