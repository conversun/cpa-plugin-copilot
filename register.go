package main

import (
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// pluginIdentifier is the provider key this plugin owns.
// It must match the "provider" field the host uses to route auth records
// and runtime executor lookups. Keep in sync with the host's github-copilot key.
const (
	pluginIdentifier = "github-copilot"
	pluginName       = "GitHub Copilot"
	pluginVersion    = "0.1.0-body-port"
	pluginAuthor     = "conversun"
	pluginRepo       = "https://github.com/conversun/cpa-plugin-copilot"
)

// registration mirrors the JSON shape the host reads from the plugin.register
// response. Field names come from examples/plugin/claude-web-search-router/go/main.go,
// which is the closest working reference for the current schema version.
type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

// registrationCapability enumerates the capability flags this plugin declares.
// Path-A adds Executor with OAuth scope; the actual executor methods are
// still stubs (see executor.go) but declaring the capability lets the host
// route Copilot models to us so we can see the real request shape.
//
// Formats intentionally list a superset: the executor bridges these through
// sdktranslator so it accepts them at ingress and emits them at egress
// (matching the requesting client's SourceFormat).
type registrationCapability struct {
	AuthProvider          bool     `json:"auth_provider"`
	Executor              bool     `json:"executor"`
	ModelProvider         bool     `json:"model_provider"`
	CommandLinePlugin     bool     `json:"command_line_plugin"`
	ManagementAPI         bool     `json:"management_api"`
	ExecutorModelScope    string   `json:"executor_model_scope,omitempty"`
	ExecutorInputFormats  []string `json:"executor_input_formats,omitempty"`
	ExecutorOutputFormats []string `json:"executor_output_formats,omitempty"`
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             pluginName,
			Version:          pluginVersion,
			Author:           pluginAuthor,
			GitHubRepository: pluginRepo,
			Logo:             "https://github.githubassets.com/images/modules/site/copilot/copilot.png",
		},
		Capabilities: registrationCapability{
			AuthProvider:          true,
			Executor:              true,
			ModelProvider:         true,
			CommandLinePlugin:     true,
			ManagementAPI:         true,
			ExecutorModelScope:    string(pluginapi.ExecutorModelScopeOAuth),
			ExecutorInputFormats:  []string{"openai", "openai-response", "claude", "gemini", "codex"},
			ExecutorOutputFormats: []string{"openai", "openai-response", "claude", "gemini", "codex"},
		},
	}
}
