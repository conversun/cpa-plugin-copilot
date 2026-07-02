// Package copilot provides GitHub Copilot authentication primitives.
// Ported from internal/auth/copilot/copilot_auth.go in the CLIProxyAPI host.
// Constructors were refactored to accept a plain *http.Client so this package
// carries no internal/config or internal/util dependency and can live in an
// out-of-tree plugin binary.
package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	// copilotAPITokenURL is the endpoint for getting Copilot API tokens from GitHub token.
	copilotAPITokenURL = "https://api.github.com/copilot_internal/v2/token"
	// copilotAPIEndpoint is the base URL for making API requests.
	copilotAPIEndpoint = "https://api.githubcopilot.com"

	// Common HTTP header values for Copilot API requests.
	// Keep these in sync with the host executor.
	copilotUserAgent     = "GitHubCopilotChat/0.50.0"
	copilotEditorVersion = "vscode/1.122.0"
	copilotPluginVersion = "copilot-chat/0.50.0"
	copilotIntegrationID = "vscode-chat"
	copilotOpenAIIntent  = "conversation-edits"
	copilotGitHubAPIVer  = "2025-04-01"
)

// CopilotAPIToken represents the Copilot API token response.
type CopilotAPIToken struct {
	// Token is the JWT token for authenticating with the Copilot API.
	Token string `json:"token"`
	// ExpiresAt is the Unix timestamp when the token expires.
	ExpiresAt int64 `json:"expires_at"`
	// Endpoints contains the available API endpoints.
	Endpoints struct {
		API           string `json:"api"`
		Proxy         string `json:"proxy"`
		OriginTracker string `json:"origin-tracker"`
		Telemetry     string `json:"telemetry"`
	} `json:"endpoints,omitempty"`
	// ErrorDetails contains error information if the request failed.
	ErrorDetails *struct {
		URL              string `json:"url"`
		Message          string `json:"message"`
		DocumentationURL string `json:"documentation_url"`
	} `json:"error_details,omitempty"`
}

// CopilotAuth handles GitHub Copilot authentication flow.
// It provides methods for device flow authentication and token management.
type CopilotAuth struct {
	httpClient   *http.Client
	deviceClient *DeviceFlowClient
}

// NewCopilotAuth creates a new CopilotAuth service instance.
// When client is nil, a default client with a 30s timeout is used.
func NewCopilotAuth(client *http.Client) *CopilotAuth {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &CopilotAuth{
		httpClient:   client,
		deviceClient: NewDeviceFlowClient(client),
	}
}

// DeviceClient returns the underlying DeviceFlowClient so plugin adapters can
// drive a single-shot ExchangeDeviceCode / FetchUserInfo pair without pulling
// the whole PollForToken loop into the plugin process.
func (c *CopilotAuth) DeviceClient() *DeviceFlowClient {
	return c.deviceClient
}

// StartDeviceFlow initiates the device flow authentication.
// Returns the device code response containing the user code and verification URI.
func (c *CopilotAuth) StartDeviceFlow(ctx context.Context) (*DeviceCodeResponse, error) {
	return c.deviceClient.RequestDeviceCode(ctx)
}

// WaitForAuthorization polls for user authorization and returns the auth bundle.
// This helper is kept for parity with the host code path; plugin callers should
// prefer the single-shot ExchangeDeviceCode + FetchUserInfo pair driven by the
// pluginapi.AuthProvider.PollLogin cycle.
func (c *CopilotAuth) WaitForAuthorization(ctx context.Context, deviceCode *DeviceCodeResponse) (*CopilotAuthBundle, error) {
	tokenData, err := c.deviceClient.PollForToken(ctx, deviceCode)
	if err != nil {
		return nil, err
	}

	userInfo, err := c.deviceClient.FetchUserInfo(ctx, tokenData.AccessToken)
	if err != nil {
		log.Warnf("copilot: failed to fetch user info: %v", err)
	}

	username := userInfo.Login
	if username == "" {
		username = "github-user"
	}

	return &CopilotAuthBundle{
		TokenData: tokenData,
		Username:  username,
		Email:     userInfo.Email,
		Name:      userInfo.Name,
	}, nil
}

// GetCopilotAPIToken exchanges a GitHub access token for a Copilot API token.
// This token is used to make authenticated requests to the Copilot API.
func (c *CopilotAuth) GetCopilotAPIToken(ctx context.Context, githubAccessToken string) (*CopilotAPIToken, error) {
	if githubAccessToken == "" {
		return nil, NewAuthenticationError(ErrTokenExchangeFailed, fmt.Errorf("github access token is empty"))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, copilotAPITokenURL, nil)
	if err != nil {
		return nil, NewAuthenticationError(ErrTokenExchangeFailed, err)
	}

	req.Header.Set("Authorization", "token "+githubAccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", copilotUserAgent)
	req.Header.Set("Editor-Version", copilotEditorVersion)
	req.Header.Set("Editor-Plugin-Version", copilotPluginVersion)
	req.Header.Set("Copilot-Integration-Id", copilotIntegrationID)
	req.Header.Set("X-Github-Api-Version", copilotGitHubAPIVer)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, NewAuthenticationError(ErrTokenExchangeFailed, err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("copilot api token: close body error: %v", errClose)
		}
	}()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, NewAuthenticationError(ErrTokenExchangeFailed, err)
	}

	if !isHTTPSuccess(resp.StatusCode) {
		return nil, NewAuthenticationError(ErrTokenExchangeFailed,
			fmt.Errorf("status %d: %s", resp.StatusCode, string(bodyBytes)))
	}

	var apiToken CopilotAPIToken
	if err = json.Unmarshal(bodyBytes, &apiToken); err != nil {
		return nil, NewAuthenticationError(ErrTokenExchangeFailed, err)
	}

	if apiToken.Token == "" {
		return nil, NewAuthenticationError(ErrTokenExchangeFailed, fmt.Errorf("empty copilot api token"))
	}

	return &apiToken, nil
}

// ValidateToken checks if a GitHub access token is valid by attempting to fetch user info.
func (c *CopilotAuth) ValidateToken(ctx context.Context, accessToken string) (bool, string, error) {
	if accessToken == "" {
		return false, "", nil
	}

	userInfo, err := c.deviceClient.FetchUserInfo(ctx, accessToken)
	if err != nil {
		return false, "", err
	}

	return true, userInfo.Login, nil
}

// CreateTokenStorage creates a new CopilotTokenStorage from auth bundle.
func (c *CopilotAuth) CreateTokenStorage(bundle *CopilotAuthBundle) *CopilotTokenStorage {
	return &CopilotTokenStorage{
		AccessToken: bundle.TokenData.AccessToken,
		TokenType:   bundle.TokenData.TokenType,
		Scope:       bundle.TokenData.Scope,
		Username:    bundle.Username,
		Email:       bundle.Email,
		Name:        bundle.Name,
		Type:        "github-copilot",
	}
}

// LoadAndValidateToken loads a token from storage and validates it.
// Returns the storage if valid, or an error if the token is invalid or expired.
func (c *CopilotAuth) LoadAndValidateToken(ctx context.Context, storage *CopilotTokenStorage) (bool, error) {
	if storage == nil || storage.AccessToken == "" {
		return false, fmt.Errorf("no token available")
	}

	apiToken, err := c.GetCopilotAPIToken(ctx, storage.AccessToken)
	if err != nil {
		return false, err
	}

	if apiToken.ExpiresAt > 0 && time.Now().Unix() >= apiToken.ExpiresAt {
		return false, fmt.Errorf("copilot api token expired")
	}

	return true, nil
}

// GetAPIEndpoint returns the Copilot API endpoint URL.
func (c *CopilotAuth) GetAPIEndpoint() string {
	return copilotAPIEndpoint
}

// MakeAuthenticatedRequest creates an authenticated HTTP request to the Copilot API.
func (c *CopilotAuth) MakeAuthenticatedRequest(ctx context.Context, method, url string, body io.Reader, apiToken *CopilotAPIToken) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+apiToken.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", copilotUserAgent)
	req.Header.Set("Editor-Version", copilotEditorVersion)
	req.Header.Set("Editor-Plugin-Version", copilotPluginVersion)
	req.Header.Set("Openai-Intent", copilotOpenAIIntent)
	req.Header.Set("Copilot-Integration-Id", copilotIntegrationID)
	req.Header.Set("X-Github-Api-Version", copilotGitHubAPIVer)

	return req, nil
}

// CopilotModelEntry represents a single model entry returned by the Copilot /models API.
type CopilotModelEntry struct {
	ID           string         `json:"id"`
	Object       string         `json:"object"`
	Created      int64          `json:"created"`
	OwnedBy      string         `json:"owned_by"`
	Name         string         `json:"name,omitempty"`
	Version      string         `json:"version,omitempty"`
	Capabilities map[string]any `json:"capabilities,omitempty"`
}

// CopilotModelLimits holds the token limits returned by the Copilot /models
// API under capabilities.limits. These limits vary by account type (individual
// vs business) and are the authoritative source for prompt-size enforcement.
type CopilotModelLimits struct {
	// MaxContextWindowTokens is the total context window (prompt + output).
	MaxContextWindowTokens int
	// MaxPromptTokens is the hard limit on input/prompt tokens.
	MaxPromptTokens int
	// MaxOutputTokens is the maximum number of output/completion tokens.
	MaxOutputTokens int
}

// Limits extracts token limits from the model's capabilities map.
// Returns nil if no limits are available or the structure is unexpected.
func (e *CopilotModelEntry) Limits() *CopilotModelLimits {
	if e.Capabilities == nil {
		return nil
	}
	limitsRaw, ok := e.Capabilities["limits"]
	if !ok {
		return nil
	}
	limitsMap, ok := limitsRaw.(map[string]any)
	if !ok {
		return nil
	}
	result := &CopilotModelLimits{
		MaxContextWindowTokens: anyToInt(limitsMap["max_context_window_tokens"]),
		MaxPromptTokens:        anyToInt(limitsMap["max_prompt_tokens"]),
		MaxOutputTokens:        anyToInt(limitsMap["max_output_tokens"]),
	}
	if result.MaxContextWindowTokens == 0 && result.MaxPromptTokens == 0 && result.MaxOutputTokens == 0 {
		return nil
	}
	return result
}

// anyToInt converts a JSON-decoded numeric value to int (encoding/json
// decodes numbers into float64 when the target is interface{}).
func anyToInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case float32:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	default:
		return 0
	}
}

// ListModelsWithGitHubToken is a convenience: it exchanges the GitHub access
// token for a Copilot API token, then fetches available models.
func (c *CopilotAuth) ListModelsWithGitHubToken(ctx context.Context, githubAccessToken string) ([]CopilotModelEntry, error) {
	apiToken, err := c.GetCopilotAPIToken(ctx, githubAccessToken)
	if err != nil {
		return nil, fmt.Errorf("copilot: failed to get API token for model listing: %w", err)
	}
	return c.ListModels(ctx, apiToken)
}

// CopilotModelsResponse represents the response from the Copilot /models endpoint.
type CopilotModelsResponse struct {
	Data   []CopilotModelEntry `json:"data"`
	Object string              `json:"object"`
}

// maxModelsResponseSize is the maximum allowed response size from the /models endpoint (2 MB).
const maxModelsResponseSize = 2 * 1024 * 1024

// allowedCopilotAPIHosts is the set of hosts that are considered safe for Copilot API requests.
var allowedCopilotAPIHosts = map[string]bool{
	"api.githubcopilot.com":               true,
	"api.individual.githubcopilot.com":    true,
	"api.business.githubcopilot.com":      true,
	"copilot-proxy.githubusercontent.com": true,
}

// ListModels fetches the list of available models from the Copilot API.
// It requires a valid Copilot API token (not the GitHub access token).
func (c *CopilotAuth) ListModels(ctx context.Context, apiToken *CopilotAPIToken) ([]CopilotModelEntry, error) {
	if apiToken == nil || apiToken.Token == "" {
		return nil, fmt.Errorf("copilot: api token is required for listing models")
	}

	modelsURL := copilotAPIEndpoint + "/models"
	if ep := strings.TrimRight(apiToken.Endpoints.API, "/"); ep != "" {
		parsed, err := url.Parse(ep)
		if err == nil && parsed.Scheme == "https" && allowedCopilotAPIHosts[parsed.Host] {
			modelsURL = ep + "/models"
		} else {
			log.Warnf("copilot: ignoring untrusted API endpoint %q, using default", ep)
		}
	}

	req, err := c.MakeAuthenticatedRequest(ctx, http.MethodGet, modelsURL, nil, apiToken)
	if err != nil {
		return nil, fmt.Errorf("copilot: failed to create models request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("copilot: models request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("copilot list models: close body error: %v", errClose)
		}
	}()

	limitedReader := io.LimitReader(resp.Body, maxModelsResponseSize)
	bodyBytes, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("copilot: failed to read models response: %w", err)
	}

	if !isHTTPSuccess(resp.StatusCode) {
		return nil, fmt.Errorf("copilot: list models failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var modelsResp CopilotModelsResponse
	if err = json.Unmarshal(bodyBytes, &modelsResp); err != nil {
		return nil, fmt.Errorf("copilot: failed to parse models response: %w", err)
	}

	return modelsResp.Data, nil
}

// isHTTPSuccess checks if the status code indicates success (2xx).
func isHTTPSuccess(statusCode int) bool {
	return statusCode >= 200 && statusCode < 300
}
