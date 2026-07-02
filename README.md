# cpa-plugin-copilot (PoC)

**Status: Path-A skeleton in place.** This repo takes the GitHub Copilot auth
code out of the CLIProxyAPI `feat/copilot` branch and repackages it as an
external CLIProxyAPI plugin against the native C ABI. `AuthProvider` is fully
implemented (Path-B PoC); `Executor` is now DECLARED as a capability with
dispatch wired, but every method body is a `not_implemented` stub — next
session ports the real 1785-LOC executor from the host's
`internal/runtime/executor/github_copilot_executor.go`. `ModelProvider`,
`ManagementAPI`, and `CommandLinePlugin` capabilities are still out of scope.

## Layout

```
cpa-plugin-copilot/
├── go.mod              # module + local replace to ../CLIProxyAPIPlus
├── main.go             # cgo C ABI + JSON-RPC dispatch
├── register.go         # plugin.register response
├── auth_provider.go    # pluginapi.AuthProvider adapter (Parse/Start/Poll/Refresh)
├── executor.go         # Path-A executor dispatch stubs (bodies pending)
├── copilot/            # GitHub Copilot auth service (ported 1:1 from host)
│   ├── copilot_auth.go
│   ├── oauth.go        # exchangeDeviceCode was exported so PollLogin can drive it
│   ├── token.go        # SaveTokenToFile dropped (host owns persistence)
│   └── errors.go
└── internal/           # Shim packages prepared for Path-A executor body port
    ├── thinking/       # ParseSuffix (verbatim) + ApplyThinking (no-op; host applies before dispatch)
    ├── registry/       # ModelInfo struct + stubs that fall through to executor's static heuristic
    └── helps/          # Tokenizer + usage/count helper stubs (real bodies land with executor port)
```

## What's in vs. out

| Capability             | Status              | Notes                                                                                                        |
| ---------------------- | ------------------- | ------------------------------------------------------------------------------------------------------------ |
| `AuthProvider.Identifier` | ✅ implemented   | Returns `github-copilot`.                                                                                    |
| `AuthProvider.ParseAuth` | ✅ implemented    | Claims files whose JSON `type` is `github-copilot`. Preserves incoming `FileName`.                           |
| `AuthProvider.StartLogin` | ✅ implemented   | Real GitHub device flow. State = `device_code`, metadata carries `user_code` + `verification_uri`.           |
| `AuthProvider.PollLogin`  | ✅ implemented   | Single-shot `ExchangeDeviceCode`; host owns the poll loop. Success also verifies Copilot subscription.       |
| `AuthProvider.RefreshAuth` | ✅ implemented  | Re-mints Copilot API token; `NextRefreshAfter` set from `apiToken.ExpiresAt` minus 60 s safety margin.       |
| `Executor.Execute` (non-streaming Chat Completions / Responses) | ✅ implemented | Full 1682-LOC executor body ported; adapter.go bridges pluginapi ↔ cliproxy types; unit-tested. |
| `Executor.ExecuteStream` | 🟡 stub | Needs `pluginabi.MethodHostStreamEmit` callback wiring to funnel `<-chan ExecutorStreamChunk` back to the host. |
| `Executor.CountTokens` | ✅ wired | Adapter path validated; helps.CountOpenAIChatTokens still returns 0 (real tiktoken port from token_helpers.go deferred). |
| `Executor.HttpRequest` | ✅ implemented | Injects Copilot auth headers, forwards via `newProxyAwareHTTPClient`. |
| `ModelProvider.StaticModels` | ✅ implemented | Curated catalog (OpenAI/Claude/Gemini families) from `internal/models`. |
| `ModelProvider.ModelsForAuth` | ✅ implemented | Live `/models` fetch via `copilot.ListModelsWithGitHubToken`; falls back to static catalog on any error. |
| `ManagementAPI` (`/copilot-quota`) | ❌ out of scope | Follow-up. |
| `CommandLinePlugin` (`--copilot-login`) | ❌ out of scope | Host CLI still works via built-in `--login=github-copilot` on the host build. |

## Known limitations (tech debt to close before merging into the plugin registry)

1. **HTTP transport bypasses the host.** `AuthLoginStartRequest.HTTPClient`,
   `AuthLoginPollRequest.HTTPClient`, and `AuthRefreshRequest.HTTPClient` are
   `json:"-"` so they never survive the JSON RPC roundtrip; the plugin falls
   back to `&http.Client{Timeout: 30s}`. That means:
   - The host's proxy config is ignored.
   - The host's request-log capture does not see plugin HTTP calls.
   The fix is to switch `newAuthService` to a callback wrapper that dispatches
   `pluginabi.MethodHostHTTPDo` through the stored host API. Not required for
   PoC verification but must land before Path A completes.
2. **`ParseAuth` does not validate the token.** A file whose `type` is
   `github-copilot` and whose `access_token` is non-empty is treated as valid.
   Actually calling `GetCopilotAPIToken` there would make parse slow (network
   RTT per file); the host already schedules a `RefreshAuth` shortly after, so
   the token is verified end-to-end within seconds of parse anyway.
3. **No unit tests.** The ported `copilot/` package inherits the host's test
   coverage on the branch it came from, but nothing is wired into `go test ./...`
   inside this repo yet.
4. **No CI matrix.** Real distribution needs macOS x64 / arm64, Linux x64 /
   arm64, Windows x64 builds — deferred to Path A.
5. **Metadata leaks the raw `access_token`.** Matches the host's
   `sdk/auth/github_copilot.go` shape for compatibility, but the executor only
   needs `api_token` at runtime. Once the executor plugin lands and the
   RefreshAuth cadence is proven, `access_token` can be scoped to
   `StorageJSON` only.

## Build

Requires the parent `CLIProxyAPIPlus` checkout to be a sibling directory
(`../CLIProxyAPIPlus`) so the `replace` in `go.mod` resolves.

```bash
# macOS: .dylib
go build -buildmode=c-shared -o cpa-plugin-copilot.dylib .

# Linux: .so
GOOS=linux go build -buildmode=c-shared -o cpa-plugin-copilot.so .

# Windows: .dll (needs a MinGW toolchain or a Windows host)
GOOS=windows go build -buildmode=c-shared -o cpa-plugin-copilot.dll .
```

## Loading the plugin locally

Add to `config.yaml`:

```yaml
plugins:
  path:
    - /absolute/path/to/CLIProxyAPI-plugin-copilot/cpa-plugin-copilot.dylib
  configs:
    github-copilot:
      enabled: true
```

Then run the host and check the pluginhost logs — the plugin should register
with `provider=github-copilot` and expose the `AuthProvider` capability.

## Verifying the PoC

Automated (no user interaction required):

- `go build -buildmode=c-shared -o cpa-plugin-copilot.dylib .` returns 0.
- `go vet ./...` returns 0.
- The compiled artifact exports `cliproxy_plugin_init`, `cliproxyPluginCall`,
  `cliproxyPluginFree`, `cliproxyPluginShutdown`
  (`nm cpa-plugin-copilot.dylib | grep cliproxy`).

Manual (needs a real GitHub account with an active Copilot subscription):

1. Drop an existing `auths/github-copilot-*.json` from the host build into
   the host's auth dir. The plugin should claim it via `ParseAuth` and the
   host should surface it in `/v0/management/auth/list`.
2. Trigger a management-API login for `github-copilot`. Watch for the
   `verification_uri` + `user_code` pair in the login start response, complete
   the device flow at `https://github.com/login/device`, and confirm the
   PollLogin transition to `success` yields a persisted
   `github-copilot-<username>.json` under the host auth dir.
3. Wait ~28 minutes past the last login/refresh and confirm the host's
   scheduled `RefreshAuth` produces a fresh `api_token` in `Metadata`.

## Roadmap (Path-A staged execution)

**Session 1 (done, this session)** — Path-B PoC + Path-A skeleton.
`AuthProvider` fully implemented; `Executor` capability declared with
`not_implemented` stubs so the host actually routes Copilot models to us.

**Session 2 (next)** — Executor body port (~1785 LOC + shims).
Dependency map from this session's analysis:

- Copy `github_copilot_executor.go` verbatim; adjust imports.
- Add `internal/thinking/` shim (ParseSuffix + no-op ApplyThinking, ~30 LOC).
- Add `internal/registry/` shim (ModelInfo struct + static Copilot model list).
- Add `internal/helps/` shim (tokenizer + count helpers, ~200 LOC).
- Copy or reimplement package-private helpers: `statusErr`,
  `metaStringValue`, `dataTag`, `parseOpenAI*Usage`, `summarizeErrorBody`.
  Replace `newProxyAwareHTTPClient` with `http.DefaultClient` (or host
  callback per limitation #1). Drop `newUsageReporter` / `recordAPI*`
  (host tracks usage at the higher `UsagePlugin` layer).
- Inline the Anthropic gateway path: replace `NewClaudeExecutor(e.cfg)`
  with a direct HTTP POST mirroring the Anthropic-compat request shape.
- Verify non-streaming Chat Completions end-to-end.

**Session 3** — Streaming + Responses API.
SSE forwarding via `pluginabi.MethodHostStreamEmit`; Responses API path
(codex models); Claude-source-format streaming translation.

**Session 4** — `ModelProvider` + `ManagementAPI`.
Move Copilot static model list into `internal/models/`, expose via
`ModelProvider.StaticModels`; live `/models` fetch behind `ModelsForAuth`;
re-expose `/copilot-quota` via `ManagementAPI`.

**Session 5** — CLI + release engineering.
Wire `--copilot-login` via `CommandLinePlugin`; switch HTTP calls to
host-side transport (`pluginabi.MethodHostHTTPDo`); add CI matrix (macOS
x64/arm64, Linux x64/arm64, Windows x64); cut v0.1.0 and submit PR to
[CLIProxyAPI-Plugins-Store](https://github.com/router-for-me/CLIProxyAPI-Plugins-Store)
`registry.json`.

## License

MIT (matching the host).
