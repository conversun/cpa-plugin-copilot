// Package copilot provides GitHub Copilot authentication primitives.
// Ported from internal/auth/copilot/token.go in the CLIProxyAPI host.
// SaveTokenToFile was intentionally dropped: plugin persistence is handled by
// the host through the pluginapi.AuthData.StorageJSON round-trip.
package copilot

// CopilotTokenStorage stores OAuth2 token information for GitHub Copilot API authentication.
// It maintains compatibility with the existing host auth system while carrying the
// Copilot-specific fields the plugin executor consumes.
type CopilotTokenStorage struct {
	// AccessToken is the OAuth2 access token used for authenticating API requests.
	AccessToken string `json:"access_token"`
	// TokenType is the type of token, typically "bearer".
	TokenType string `json:"token_type"`
	// Scope is the OAuth2 scope granted to the token.
	Scope string `json:"scope"`
	// ExpiresAt is the timestamp when the access token expires (if provided).
	ExpiresAt string `json:"expires_at,omitempty"`
	// Username is the GitHub username associated with this token.
	Username string `json:"username"`
	// Email is the GitHub email address associated with this token.
	Email string `json:"email,omitempty"`
	// Name is the GitHub display name associated with this token.
	Name string `json:"name,omitempty"`
	// Type indicates the authentication provider type, always "github-copilot" for this storage.
	Type string `json:"type"`
}

// CopilotTokenData holds the raw OAuth token response from GitHub.
type CopilotTokenData struct {
	// AccessToken is the OAuth2 access token.
	AccessToken string `json:"access_token"`
	// TokenType is the type of token, typically "bearer".
	TokenType string `json:"token_type"`
	// Scope is the OAuth2 scope granted to the token.
	Scope string `json:"scope"`
}

// CopilotAuthBundle bundles authentication data returned by a successful device flow.
type CopilotAuthBundle struct {
	// TokenData contains the OAuth token information.
	TokenData *CopilotTokenData
	// Username is the GitHub username.
	Username string
	// Email is the GitHub email address.
	Email string
	// Name is the GitHub display name.
	Name string
}

// DeviceCodeResponse represents GitHub's device code response.
type DeviceCodeResponse struct {
	// DeviceCode is the device verification code.
	DeviceCode string `json:"device_code"`
	// UserCode is the code the user must enter at the verification URI.
	UserCode string `json:"user_code"`
	// VerificationURI is the URL where the user should enter the code.
	VerificationURI string `json:"verification_uri"`
	// ExpiresIn is the number of seconds until the device code expires.
	ExpiresIn int `json:"expires_in"`
	// Interval is the minimum number of seconds to wait between polling requests.
	Interval int `json:"interval"`
}
