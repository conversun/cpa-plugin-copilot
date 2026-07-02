package main

// command_line.go wires the CommandLinePlugin capability.
//
// The plugin registers a single flag, `--copilot-login`, that drives an
// interactive GitHub device flow on stdout. On success the resulting
// pluginapi.AuthData is returned in CommandLineExecutionResponse.Auths so
// the host persists it under the usual github-copilot-<username>.json path.
//
// This complements the management-API OAuth path (auth_provider.go). Some
// deployments prefer a plain CLI login (SSH terminals, tmux sessions, CI
// image bootstraps); this handler covers that surface.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/conversun/cpa-plugin-copilot/copilot"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	// copilotLoginFlagName is the flag the host exposes in --help. Mirrors
	// gemini-cli's --geminicli-login convention.
	copilotLoginFlagName = "copilot-login"

	// cliPollInterval is how often the CLI polls GitHub's token endpoint.
	cliPollInterval = 5 * time.Second

	// cliPollTimeout caps the CLI wait so a forgotten terminal does not
	// hang forever. Matches copilot.maxPollDuration in the auth package.
	cliPollTimeout = 15 * time.Minute
)

// handleCommandLineRegister answers command_line.register by declaring the
// plugin's flags. The host merges these into its own flag set and shows
// them in `--help`.
func handleCommandLineRegister(_ context.Context, _ []byte) ([]byte, error) {
	return okEnvelope(pluginapi.CommandLineRegistrationResponse{
		Flags: []pluginapi.CommandLineFlag{
			{
				Name:         copilotLoginFlagName,
				Usage:        "Interactively authenticate with GitHub Copilot via device flow.",
				Type:         "bool",
				DefaultValue: "false",
			},
		},
	})
}

// handleCommandLineExecute answers command_line.execute. If --copilot-login
// is set, we drive the full device flow synchronously (stdout progress +
// blocking poll) and return the resulting AuthData in Auths[]. Otherwise
// we return an empty response and let other plugin handlers act on their
// own triggered flags.
func handleCommandLineExecute(ctx context.Context, raw []byte) ([]byte, error) {
	var req pluginapi.CommandLineExecutionRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}

	// Only act when our flag was actually triggered by the user.
	flag, ok := req.TriggeredFlags[copilotLoginFlagName]
	if !ok || !flag.Set || strings.EqualFold(flag.Value, "false") {
		return okEnvelope(pluginapi.CommandLineExecutionResponse{})
	}

	authData, stdout, stderr, exitCode := runInteractiveLogin(ctx)
	resp := pluginapi.CommandLineExecutionResponse{
		Stdout:   []byte(stdout),
		Stderr:   []byte(stderr),
		ExitCode: exitCode,
	}
	if authData != nil {
		resp.Auths = []pluginapi.AuthData{*authData}
	}
	return okEnvelope(resp)
}

// runInteractiveLogin walks the device flow end-to-end and returns the
// bundled AuthData plus stdout/stderr/exit code to hand back through the
// pluginapi.CommandLineExecutionResponse envelope.
//
// The polling is a single-shot ExchangeDeviceCode loop driven by cliPollInterval
// with slow_down back-off: matches copilot.PollForToken semantics but keeps
// the polling logic here so the CLI can render progress lines.
func runInteractiveLogin(ctx context.Context) (*pluginapi.AuthData, string, string, int) {
	var out strings.Builder
	var errOut strings.Builder

	svc := copilot.NewCopilotAuth(nil)

	fmt.Fprintln(&out, "Starting GitHub Copilot authentication (device flow)...")
	deviceCode, err := svc.StartDeviceFlow(ctx)
	if err != nil {
		fmt.Fprintf(&errOut, "github-copilot: failed to start device flow: %v\n", err)
		return nil, out.String(), errOut.String(), 1
	}
	fmt.Fprintf(&out, "\nTo authenticate, visit: %s\n", deviceCode.VerificationURI)
	fmt.Fprintf(&out, "Enter the code: %s\n", deviceCode.UserCode)
	fmt.Fprintf(&out, "(Waits up to %s for authorization.)\n\n", cliPollTimeout)

	tokenData, err := pollForToken(ctx, svc, deviceCode)
	if err != nil {
		fmt.Fprintf(&errOut, "github-copilot: %s\n", copilot.GetUserFriendlyMessage(err))
		return nil, out.String(), errOut.String(), 1
	}

	userInfo, userErr := svc.DeviceClient().FetchUserInfo(ctx, tokenData.AccessToken)
	if userErr != nil {
		fmt.Fprintf(&out, "warning: could not fetch GitHub user profile: %v\n", userErr)
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

	// Verify Copilot subscription is active before persisting the record.
	apiToken, apiErr := svc.GetCopilotAPIToken(ctx, tokenData.AccessToken)
	if apiErr != nil {
		fmt.Fprintf(&errOut, "github-copilot: subscription verification failed: %v\n", apiErr)
		return nil, out.String(), errOut.String(), 1
	}

	storage := svc.CreateTokenStorage(bundle)
	authData := buildAuthData(storage, apiToken, "")
	fmt.Fprintf(&out, "\nGitHub Copilot authentication successful for user: %s\n", bundle.Username)
	return &authData, out.String(), errOut.String(), 0
}

// pollForToken loops ExchangeDeviceCode until success, timeout, or a
// terminal OAuth error. Encapsulated here so runInteractiveLogin stays
// focused on rendering; also lets tests probe the loop without stdout.
func pollForToken(ctx context.Context, svc *copilot.CopilotAuth, deviceCode *copilot.DeviceCodeResponse) (*copilot.CopilotTokenData, error) {
	interval := time.Duration(deviceCode.Interval) * time.Second
	if interval < cliPollInterval {
		interval = cliPollInterval
	}
	deadline := time.Now().Add(cliPollTimeout)
	if deviceCode.ExpiresIn > 0 {
		codeDeadline := time.Now().Add(time.Duration(deviceCode.ExpiresIn) * time.Second)
		if codeDeadline.Before(deadline) {
			deadline = codeDeadline
		}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, copilot.NewAuthenticationError(copilot.ErrPollingTimeout, ctx.Err())
		case <-ticker.C:
			if time.Now().After(deadline) {
				return nil, copilot.ErrPollingTimeout
			}

			token, err := svc.DeviceClient().ExchangeDeviceCode(ctx, deviceCode.DeviceCode)
			if err == nil {
				return token, nil
			}

			var authErr *copilot.AuthenticationError
			if errors.As(err, &authErr) {
				switch authErr.Type {
				case copilot.ErrAuthorizationPending.Type:
					continue
				case copilot.ErrSlowDown.Type:
					interval += 5 * time.Second
					ticker.Reset(interval)
					continue
				case copilot.ErrDeviceCodeExpired.Type, copilot.ErrAccessDenied.Type:
					return nil, err
				}
			}
			return nil, err
		}
	}
}
