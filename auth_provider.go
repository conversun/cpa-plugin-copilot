package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/conversun/cpa-plugin-copilot/copilot"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

// authTypeMarker is the JSON `type` field the host writes to github-copilot
// storage files. ParseAuth uses it as the sole ownership signal so the plugin
// does not accidentally claim files belonging to other providers.
const authTypeMarker = "github-copilot"

// refreshSafetyMargin is subtracted from the Copilot API token's advertised
// expiry when computing NextRefreshAfter. A ~30-minute API token minus a
// 60-second margin gives the host enough headroom to refresh before an
// in-flight execute request has to retry.
const refreshSafetyMargin = 60 * time.Second

// pollTracker rate-limits per-device-code polls against GitHub. The management
// panel polls every ~3s regardless of provider, but GitHub's device flow
// requires respecting the interval it returns (default 5s, extended to 350s+
// after a slow_down penalty). Hitting GitHub every 3s triggers slow_down that
// never clears, so we cache the last real poll time per device_code and
// short-circuit to Pending when the panel polls faster than allowed.
var (
	pollTrackerMu sync.Mutex
	pollTracker   = map[string]pollState{}
)

type pollState struct {
	lastPollAt time.Time
	interval   time.Duration
}

const (
	pollIntervalDefault = 5 * time.Second
	pollIntervalMax     = 60 * time.Second
)

// pollRateLimit returns true (and the caller must short-circuit to Pending)
// when the caller is polling faster than GitHub allows. It also records the
// caller's current attempt so the next call's gate is correctly computed.
func pollRateLimit(state string, defaultInterval time.Duration) bool {
	if defaultInterval <= 0 {
		defaultInterval = pollIntervalDefault
	}
	pollTrackerMu.Lock()
	defer pollTrackerMu.Unlock()
	record, seen := pollTracker[state]
	if !seen {
		pollTracker[state] = pollState{lastPollAt: time.Now(), interval: defaultInterval}
		return false
	}
	interval := record.interval
	if interval < defaultInterval {
		interval = defaultInterval
	}
	if time.Since(record.lastPollAt) < interval {
		return true
	}
	record.lastPollAt = time.Now()
	pollTracker[state] = record
	return false
}

// pollExtendInterval bumps the tracked interval after GitHub returned
// slow_down, so subsequent polls back off. The interval doubles until it
// caps at pollIntervalMax; per-state so multiple concurrent logins don't
// interfere.
func pollExtendInterval(state string) {
	pollTrackerMu.Lock()
	defer pollTrackerMu.Unlock()
	record := pollTracker[state]
	if record.interval <= 0 {
		record.interval = pollIntervalDefault
	}
	record.interval *= 2
	if record.interval > pollIntervalMax {
		record.interval = pollIntervalMax
	}
	pollTracker[state] = record
}

// pollForget releases the tracker slot on terminal (success/error) responses.
func pollForget(state string) {
	pollTrackerMu.Lock()
	delete(pollTracker, state)
	pollTrackerMu.Unlock()
}

// newAuthService returns a copilot.CopilotAuth built on the host-provided
// HTTPClient when available. The pluginapi transport is `json:"-"`, so an
// HTTPClient never survives the JSON RPC roundtrip and this always falls back
// to a default net/http client in the current PoC. Wiring the host callback
// path (pluginabi.MethodHostHTTPDo) is a follow-up task tracked in the README.
func newAuthService(_ pluginapi.HostHTTPClient) *copilot.CopilotAuth {
	return copilot.NewCopilotAuth(&http.Client{Timeout: 30 * time.Second})
}

// handleAuthParse decides whether the plugin owns an on-disk auth file. It
// probes only the `type` field — malformed JSON or any other value is treated
// as "not ours" so the host can keep asking other providers.
func handleAuthParse(_ context.Context, raw []byte) ([]byte, error) {
	var req pluginapi.AuthParseRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	if len(req.RawJSON) == 0 {
		return okEnvelope(pluginapi.AuthParseResponse{Handled: false})
	}

	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(req.RawJSON, &probe); err != nil || probe.Type != authTypeMarker {
		return okEnvelope(pluginapi.AuthParseResponse{Handled: false})
	}

	var storage copilot.CopilotTokenStorage
	if err := json.Unmarshal(req.RawJSON, &storage); err != nil {
		return okEnvelope(pluginapi.AuthParseResponse{Handled: false})
	}
	if storage.AccessToken == "" {
		// The `type` field claimed it was ours, but the payload is unusable.
		// Better to surface as Handled=true with a hint than silently drop.
		return okEnvelope(pluginapi.AuthParseResponse{Handled: false})
	}

	authData := buildAuthData(&storage, nil, req.FileName)
	return okEnvelope(pluginapi.AuthParseResponse{Handled: true, Auth: authData})
}

// handleAuthLoginStart kicks off the GitHub device flow and hands the host
// back the URL + user code the human must enter. All state needed to resume
// polling later is either stashed in the opaque `State` field (the device
// code) or in `Metadata` (interval, user code, verification URI). The plugin
// process itself keeps no side state so plugin reloads mid-flow are safe.
func handleAuthLoginStart(ctx context.Context, raw []byte) ([]byte, error) {
	var req pluginapi.AuthLoginStartRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}

	authSvc := newAuthService(req.HTTPClient)
	deviceCode, err := authSvc.StartDeviceFlow(ctx)
	if err != nil {
		return nil, fmt.Errorf("github-copilot: start device flow: %w", err)
	}

	expiresAt := time.Now().Add(time.Duration(deviceCode.ExpiresIn) * time.Second)

	fmt.Printf("\n=================================\n GITHUB COPILOT LOGIN\n USER CODE: %s\n VISIT:     %s\n=================================\n", deviceCode.UserCode, deviceCode.VerificationURI)
	log.Infof("copilot login started: user_code=%s verification_uri=%s", deviceCode.UserCode, deviceCode.VerificationURI)

	return okEnvelope(pluginapi.AuthLoginStartResponse{
		Provider:  authTypeMarker,
		URL:       deviceCode.VerificationURI + "#code=" + url.PathEscape(deviceCode.UserCode),
		State:     deviceCode.DeviceCode,
		ExpiresAt: expiresAt,
		Metadata: map[string]any{
			"user_code":        deviceCode.UserCode,
			"verification_uri": deviceCode.VerificationURI,
			"interval":         deviceCode.Interval,
			"expires_in":       deviceCode.ExpiresIn,
		},
	})
}

// handleAuthLoginPoll issues ONE token-exchange attempt against GitHub and
// maps the OAuth error taxonomy onto pluginapi.AuthLoginStatus. The host owns
// the poll loop; on success we also fetch the GitHub user profile and one
// Copilot API token so the returned AuthData is immediately executor-ready.
func handleAuthLoginPoll(ctx context.Context, raw []byte) ([]byte, error) {
	var req pluginapi.AuthLoginPollRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}

	if req.State == "" {
		return okEnvelope(pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusError,
			Message: "missing device_code state",
		})
	}

	// Rate-limit gate: honor GitHub's requested interval so the panel's ~3s
	// polls don't get punished with slow_down/350s intervals.
	interval := pollIntervalDefault
	if req.Metadata != nil {
		if v, ok := req.Metadata["interval"].(float64); ok && v > 0 {
			interval = time.Duration(v) * time.Second
		}
	}
	if pollRateLimit(req.State, interval) {
		return okEnvelope(pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusPending,
			Message: "awaiting authorization",
		})
	}

	authSvc := newAuthService(req.HTTPClient)
	tokenData, exchangeErr := authSvc.DeviceClient().ExchangeDeviceCode(ctx, req.State)
	if exchangeErr != nil {
		return mapPollError(req.State, exchangeErr), nil
	}

	userInfo, userErr := authSvc.DeviceClient().FetchUserInfo(ctx, tokenData.AccessToken)
	if userErr != nil {
		// Fall back to a placeholder username so the user still gets an auth
		// they can rename later, instead of losing the successful login to a
		// transient profile lookup.
		log.Warnf("copilot: failed to fetch user info: %v", userErr)
	}

	bundle := &copilot.CopilotAuthBundle{
		TokenData: tokenData,
		Username:  userInfo.Login,
		Email:     userInfo.Email,
		Name:      userInfo.Name,
	}
	if bundle.Username == "" {
		bundle.Username = "github-user"
	}

	storage := authSvc.CreateTokenStorage(bundle)

	// Also verify Copilot subscription is active. This is the same guard the
	// host CLI performed before returning "authentication successful".
	apiToken, apiErr := authSvc.GetCopilotAPIToken(ctx, tokenData.AccessToken)
	if apiErr != nil {
		pollForget(req.State)
		return okEnvelope(pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusError,
			Message: fmt.Sprintf("copilot subscription verification failed: %v", apiErr),
		})
	}

	pollForget(req.State)
	authData := buildAuthData(storage, apiToken, "")
	return okEnvelope(pluginapi.AuthLoginPollResponse{
		Status:  pluginapi.AuthLoginStatusSuccess,
		Message: fmt.Sprintf("GitHub Copilot login complete: %s", bundle.Username),
		Auth:    authData,
	})
}

// handleAuthRefresh re-mints the ~30-minute Copilot API token from the stored
// GitHub access token. GitHub access tokens themselves do not expire on a
// timer; a failure here therefore means the user revoked access, their
// subscription lapsed, or GitHub is refusing us for policy reasons.
func handleAuthRefresh(ctx context.Context, raw []byte) ([]byte, error) {
	var req pluginapi.AuthRefreshRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}

	var storage copilot.CopilotTokenStorage
	if len(req.StorageJSON) == 0 {
		return nil, errors.New("github-copilot: empty storage payload")
	}
	if err := json.Unmarshal(req.StorageJSON, &storage); err != nil {
		return nil, fmt.Errorf("github-copilot: decode storage: %w", err)
	}
	if storage.AccessToken == "" {
		return nil, errors.New("github-copilot: missing access_token in storage")
	}

	authSvc := newAuthService(req.HTTPClient)
	apiToken, err := authSvc.GetCopilotAPIToken(ctx, storage.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("github-copilot: refresh api token: %w", err)
	}

	authData := buildAuthData(&storage, apiToken, "")

	nextRefresh := time.Time{}
	if apiToken.ExpiresAt > 0 {
		expiry := time.Unix(apiToken.ExpiresAt, 0)
		nextRefresh = expiry.Add(-refreshSafetyMargin)
	}

	return okEnvelope(pluginapi.AuthRefreshResponse{
		Auth:             authData,
		NextRefreshAfter: nextRefresh,
	})
}

// buildAuthData is the single place where CopilotTokenStorage becomes the
// pluginapi.AuthData that the host serialises into an auth record. Any change
// to metadata key names must be mirrored in the executor's AuthMetadata reads
// once the executor capability lands.
func buildAuthData(storage *copilot.CopilotTokenStorage, apiToken *copilot.CopilotAPIToken, existingFileName string) pluginapi.AuthData {
	username := storage.Username
	if username == "" {
		username = "github-user"
	}
	fileName := existingFileName
	if fileName == "" {
		fileName = fmt.Sprintf("github-copilot-%s.json", username)
	}
	label := storage.Email
	if label == "" {
		label = username
	}

	metadata := map[string]any{
		"type":         authTypeMarker,
		"username":     username,
		"email":        storage.Email,
		"name":         storage.Name,
		"access_token": storage.AccessToken,
		"token_type":   storage.TokenType,
		"scope":        storage.Scope,
		"timestamp":    time.Now().UnixMilli(),
	}
	if apiToken != nil {
		if apiToken.Token != "" {
			metadata["api_token"] = apiToken.Token
		}
		if apiToken.ExpiresAt > 0 {
			metadata["api_token_expires_at"] = apiToken.ExpiresAt
		}
		if apiToken.Endpoints.API != "" {
			metadata["api_endpoint"] = apiToken.Endpoints.API
		}
	}

	storageJSON, err := json.Marshal(storage)
	if err != nil {
		// Marshal of the small storage struct cannot fail in practice, but if
		// it ever does we want the failure visible rather than silently
		// producing an auth with nil storage.
		log.Warnf("copilot: failed to marshal token storage: %v", err)
	}

	return pluginapi.AuthData{
		Provider:    authTypeMarker,
		ID:          fileName,
		FileName:    fileName,
		Label:       label,
		StorageJSON: storageJSON,
		Metadata:    metadata,
	}
}

// mapPollError translates an ExchangeDeviceCode error into a Poll response
// envelope so the host can act on it without knowing the copilot error types.
// Anything not explicitly recognised is surfaced as Error rather than swallowed.
func mapPollError(state string, err error) []byte {
	var authErr *copilot.AuthenticationError
	if errors.As(err, &authErr) {
		switch authErr.Type {
		case copilot.ErrAuthorizationPending.Type:
			raw, _ := okEnvelope(pluginapi.AuthLoginPollResponse{
				Status:  pluginapi.AuthLoginStatusPending,
				Message: authErr.Message,
			})
			return raw
		case copilot.ErrSlowDown.Type:
			// GitHub asked us to back off. Extend our local interval so subsequent
			// panel polls short-circuit until the penalty clears.
			pollExtendInterval(state)
			raw, _ := okEnvelope(pluginapi.AuthLoginPollResponse{
				Status:  pluginapi.AuthLoginStatusPending,
				Message: authErr.Message,
			})
			return raw
		case copilot.ErrDeviceCodeExpired.Type, copilot.ErrAccessDenied.Type:
			pollForget(state)
			raw, _ := okEnvelope(pluginapi.AuthLoginPollResponse{
				Status:  pluginapi.AuthLoginStatusError,
				Message: copilot.GetUserFriendlyMessage(err),
			})
			return raw
		}
	}
	pollForget(state)
	raw, _ := okEnvelope(pluginapi.AuthLoginPollResponse{
		Status:  pluginapi.AuthLoginStatusError,
		Message: err.Error(),
	})
	return raw
}
