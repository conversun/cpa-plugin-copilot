// Package helps is a working port of the subset of the host's
// internal/runtime/executor/helps API that github_copilot_executor.go
// references through its private wrappers (parseOpenAIUsage,
// parseOpenAIStreamUsage, TokenizerForModel, CountOpenAIChatTokens,
// BuildOpenAIUsageJSON).
//
// Ported functionality (matches host semantics 1:1 modulo the Detail
// field renames — see the Detail comment below):
//   - TokenizerForModel: real tiktoken model-family dispatch with a
//     sync.Map cache. Claude-family / kiro / amazonq models get a 1.1x
//     adjustment factor because tiktoken underestimates non-OpenAI models.
//   - CountOpenAIChatTokens: walks the OpenAI Chat Completions payload
//     (messages, tools, functions, tool_choice, response_format, input,
//     prompt) and sums per-segment tokens.
//   - ParseOpenAIUsage / ParseOpenAIStreamUsage: gjson walkers over the
//     `usage` node (supports both prompt_tokens/input_tokens and both
//     cached / reasoning detail shapes).
//   - BuildOpenAIUsageJSON: produces `{"usage":{"prompt_tokens":N,...}}`
//     downstream translators accept.
//
// The Claude payload walker (CountClaudeChatTokens) is intentionally left
// as a stub because github_copilot_executor.go translates Claude → OpenAI
// before counting, so it is never reached.
package helps

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/tidwall/gjson"
	"github.com/tiktoken-go/tokenizer"
)

// Detail is the plugin-local subset of the host's usage.Detail. Field names
// are renamed to align with what the ported executor's private wrappers
// consume (PromptTokens / CompletionTokens / TotalTokens ...) rather than
// the host's InputTokens / OutputTokens shape — either mapping is
// equivalent, this one keeps executor call sites unchanged.
type Detail struct {
	TotalTokens       int64
	PromptTokens      int64
	CompletionTokens  int64
	CachedInputTokens int64
	ReasoningTokens   int64
}

// tokenizerCache stores tokenizer.Codec instances keyed by sanitized model
// name so we do not pay the codec-load cost per request.
var tokenizerCache sync.Map

// adjustedTokenizer wraps a tokenizer.Codec with a multiplicative
// adjustment factor. Used for Claude / kiro / amazonq family models where
// tiktoken systematically underestimates.
type adjustedTokenizer struct {
	tokenizer.Codec
	adjustmentFactor float64
}

// Count applies the adjustment factor to the underlying tokenizer's count.
func (tw *adjustedTokenizer) Count(text string) (int, error) {
	count, err := tw.Codec.Count(text)
	if err != nil {
		return 0, err
	}
	if tw.adjustmentFactor > 0 && tw.adjustmentFactor != 1.0 {
		return int(float64(count) * tw.adjustmentFactor), nil
	}
	return count, nil
}

// TokenizerForModel returns a tokenizer.Codec suitable for the given model
// identifier. Results are cached; the same model returns the same codec
// on every call. Verbatim port of the host's dispatch table.
func TokenizerForModel(model string) (tokenizer.Codec, error) {
	sanitized := strings.ToLower(strings.TrimSpace(model))
	if cached, ok := tokenizerCache.Load(sanitized); ok {
		return cached.(tokenizer.Codec), nil
	}
	enc, err := tokenizerForModel(sanitized)
	if err != nil {
		return nil, err
	}
	actual, _ := tokenizerCache.LoadOrStore(sanitized, enc)
	return actual.(tokenizer.Codec), nil
}

// tokenizerForModel dispatches to the correct tiktoken codec by model
// family. Falls back to o200k_base for anything unrecognized.
func tokenizerForModel(sanitized string) (tokenizer.Codec, error) {
	if sanitized == "" {
		return tokenizer.Get(tokenizer.Cl100kBase)
	}
	// Claude-family / kiro / amazonq models: tiktoken underestimates by
	// ~10%, so wrap with an adjustment factor.
	if strings.Contains(sanitized, "claude") || strings.HasPrefix(sanitized, "kiro-") || strings.HasPrefix(sanitized, "amazonq-") {
		enc, err := tokenizer.Get(tokenizer.Cl100kBase)
		if err != nil {
			return nil, err
		}
		return &adjustedTokenizer{Codec: enc, adjustmentFactor: 1.1}, nil
	}
	switch {
	case strings.HasPrefix(sanitized, "gpt-5"):
		return tokenizer.ForModel(tokenizer.GPT5)
	case strings.HasPrefix(sanitized, "gpt-4.1"):
		return tokenizer.ForModel(tokenizer.GPT41)
	case strings.HasPrefix(sanitized, "gpt-4o"):
		return tokenizer.ForModel(tokenizer.GPT4o)
	case strings.HasPrefix(sanitized, "gpt-4"):
		return tokenizer.ForModel(tokenizer.GPT4)
	case strings.HasPrefix(sanitized, "gpt-3.5"), strings.HasPrefix(sanitized, "gpt-3"):
		return tokenizer.ForModel(tokenizer.GPT35Turbo)
	case strings.HasPrefix(sanitized, "o1"):
		return tokenizer.ForModel(tokenizer.O1)
	case strings.HasPrefix(sanitized, "o3"):
		return tokenizer.ForModel(tokenizer.O3)
	case strings.HasPrefix(sanitized, "o4"):
		return tokenizer.ForModel(tokenizer.O4Mini)
	default:
		return tokenizer.Get(tokenizer.O200kBase)
	}
}

// CountOpenAIChatTokens approximates prompt tokens for an OpenAI Chat
// Completions payload. Walks messages, tools, functions, tool_choice,
// response_format, and top-level input/prompt fields; concatenates the
// text segments and encodes them with the model's tokenizer.
func CountOpenAIChatTokens(enc tokenizer.Codec, payload []byte) (int64, error) {
	if enc == nil {
		return 0, fmt.Errorf("encoder is nil")
	}
	if len(payload) == 0 {
		return 0, nil
	}

	root := gjson.ParseBytes(payload)
	segments := make([]string, 0, 32)

	collectOpenAIMessages(root.Get("messages"), &segments)
	collectOpenAITools(root.Get("tools"), &segments)
	collectOpenAIFunctions(root.Get("functions"), &segments)
	collectOpenAIToolChoice(root.Get("tool_choice"), &segments)
	collectOpenAIResponseFormat(root.Get("response_format"), &segments)
	addIfNotEmpty(&segments, root.Get("input").String())
	addIfNotEmpty(&segments, root.Get("prompt").String())

	joined := strings.TrimSpace(strings.Join(segments, "\n"))
	if joined == "" {
		return 0, nil
	}
	count, err := enc.Count(joined)
	if err != nil {
		return 0, err
	}
	return int64(count), nil
}

// CountClaudeChatTokens is intentionally a stub returning 0. The ported
// github_copilot_executor.go translates Claude payloads to OpenAI format
// before calling CountTokens, so this function is never reached in the
// plugin's execution path. Present only so a future caller (or a shim
// consumer of this package) does not need to guard against a missing
// symbol.
func CountClaudeChatTokens(enc tokenizer.Codec, payload []byte) (int64, error) {
	_ = enc
	_ = payload
	return 0, nil
}

// BuildOpenAIUsageJSON returns a small `{"usage":{...}}` blob matching
// what host translators consume. The plugin passes only the total count;
// downstream translators use total_tokens as prompt_tokens for the
// count-token flow.
func BuildOpenAIUsageJSON(count int64) []byte {
	return []byte(fmt.Sprintf(`{"usage":{"prompt_tokens":%d,"completion_tokens":0,"total_tokens":%d}}`, count, count))
}

// ParseOpenAIUsage walks the payload's `usage` node and returns a Detail
// populated from whichever OpenAI style shape is present (either
// prompt/completion_tokens or input/output_tokens; either
// prompt_tokens_details or input_tokens_details for cached; either
// completion_tokens_details or output_tokens_details for reasoning).
// Returns a zero Detail when no known usage shape is present.
func ParseOpenAIUsage(data []byte) Detail {
	usageNode := gjson.ParseBytes(data).Get("usage")
	if !hasOpenAIStyleUsageTokenFields(usageNode) {
		return Detail{}
	}
	return parseOpenAIStyleUsageNode(usageNode)
}

// ParseOpenAIStreamUsage extracts a Detail from a single SSE data line.
// Returns (zero, false) when the line has no `usage` node so callers can
// distinguish the "no usage yet" case from a genuinely zero count.
func ParseOpenAIStreamUsage(line []byte) (Detail, bool) {
	payload := ssePayload(line)
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return Detail{}, false
	}
	usageNode := gjson.GetBytes(payload, "usage")
	if !hasOpenAIStyleUsageTokenFields(usageNode) {
		return Detail{}, false
	}
	return parseOpenAIStyleUsageNode(usageNode), true
}

// hasOpenAIStyleUsageTokenFields reports whether the usage node has any
// of the OpenAI-family token fields worth extracting.
func hasOpenAIStyleUsageTokenFields(usageNode gjson.Result) bool {
	if !usageNode.Exists() || !usageNode.IsObject() {
		return false
	}
	return usageNode.Get("prompt_tokens").Exists() ||
		usageNode.Get("input_tokens").Exists() ||
		usageNode.Get("completion_tokens").Exists() ||
		usageNode.Get("output_tokens").Exists() ||
		usageNode.Get("total_tokens").Exists() ||
		usageNode.Get("prompt_tokens_details.cached_tokens").Exists() ||
		usageNode.Get("input_tokens_details.cached_tokens").Exists() ||
		usageNode.Get("completion_tokens_details.reasoning_tokens").Exists() ||
		usageNode.Get("output_tokens_details.reasoning_tokens").Exists()
}

// parseOpenAIStyleUsageNode extracts a Detail from a validated usage node.
// Prefers legacy fields (prompt_tokens/completion_tokens); falls back to
// the newer input_tokens/output_tokens shape when the legacy ones are
// missing.
func parseOpenAIStyleUsageNode(usageNode gjson.Result) Detail {
	prompt := usageNode.Get("prompt_tokens")
	if !prompt.Exists() {
		prompt = usageNode.Get("input_tokens")
	}
	completion := usageNode.Get("completion_tokens")
	if !completion.Exists() {
		completion = usageNode.Get("output_tokens")
	}
	detail := Detail{
		PromptTokens:     prompt.Int(),
		CompletionTokens: completion.Int(),
		TotalTokens:      usageNode.Get("total_tokens").Int(),
	}
	cached := usageNode.Get("prompt_tokens_details.cached_tokens")
	if !cached.Exists() {
		cached = usageNode.Get("input_tokens_details.cached_tokens")
	}
	if cached.Exists() {
		detail.CachedInputTokens = cached.Int()
	}
	reasoning := usageNode.Get("completion_tokens_details.reasoning_tokens")
	if !reasoning.Exists() {
		reasoning = usageNode.Get("output_tokens_details.reasoning_tokens")
	}
	if reasoning.Exists() {
		detail.ReasoningTokens = reasoning.Int()
	}
	if detail.TotalTokens == 0 {
		detail.TotalTokens = detail.PromptTokens + detail.CompletionTokens
	}
	return detail
}

// ssePayload strips the `data:` SSE prefix (and optional whitespace) from
// a line. Returns the raw bytes when no prefix is present so callers can
// pass either whole SSE lines or bare JSON.
func ssePayload(line []byte) []byte {
	trimmed := strings.TrimSpace(string(line))
	if strings.HasPrefix(trimmed, "data:") {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
	}
	if trimmed == "" || trimmed == "[DONE]" {
		return nil
	}
	return []byte(trimmed)
}

// collectOpenAIMessages walks the messages array, adding role/name/content
// segments plus tool call / function call payloads.
func collectOpenAIMessages(messages gjson.Result, segments *[]string) {
	if !messages.Exists() || !messages.IsArray() {
		return
	}
	messages.ForEach(func(_, message gjson.Result) bool {
		addIfNotEmpty(segments, message.Get("role").String())
		addIfNotEmpty(segments, message.Get("name").String())
		collectOpenAIContent(message.Get("content"), segments)
		collectOpenAIToolCalls(message.Get("tool_calls"), segments)
		collectOpenAIFunctionCall(message.Get("function_call"), segments)
		return true
	})
}

// collectOpenAIContent handles all three content shapes: plain string,
// content-part array (text/image_url/audio/tool_result), or raw JSON.
func collectOpenAIContent(content gjson.Result, segments *[]string) {
	if !content.Exists() {
		return
	}
	if content.Type == gjson.String {
		addIfNotEmpty(segments, content.String())
		return
	}
	if content.IsArray() {
		content.ForEach(func(_, part gjson.Result) bool {
			partType := part.Get("type").String()
			switch partType {
			case "text", "input_text", "output_text":
				addIfNotEmpty(segments, part.Get("text").String())
			case "image_url":
				addIfNotEmpty(segments, part.Get("image_url.url").String())
			case "input_audio", "output_audio", "audio":
				addIfNotEmpty(segments, part.Get("id").String())
			case "tool_result":
				addIfNotEmpty(segments, part.Get("name").String())
				collectOpenAIContent(part.Get("content"), segments)
			default:
				if part.IsArray() {
					collectOpenAIContent(part, segments)
					return true
				}
				if part.Type == gjson.JSON {
					addIfNotEmpty(segments, part.Raw)
					return true
				}
				addIfNotEmpty(segments, part.String())
			}
			return true
		})
		return
	}
	if content.Type == gjson.JSON {
		addIfNotEmpty(segments, content.Raw)
	}
}

// collectOpenAIToolCalls extracts tool call id/type + nested function
// name/description/arguments/parameters.
func collectOpenAIToolCalls(calls gjson.Result, segments *[]string) {
	if !calls.Exists() || !calls.IsArray() {
		return
	}
	calls.ForEach(func(_, call gjson.Result) bool {
		addIfNotEmpty(segments, call.Get("id").String())
		addIfNotEmpty(segments, call.Get("type").String())
		function := call.Get("function")
		if function.Exists() {
			addIfNotEmpty(segments, function.Get("name").String())
			addIfNotEmpty(segments, function.Get("description").String())
			addIfNotEmpty(segments, function.Get("arguments").String())
			if params := function.Get("parameters"); params.Exists() {
				addIfNotEmpty(segments, params.Raw)
			}
		}
		return true
	})
}

// collectOpenAIFunctionCall extracts legacy function_call name + args.
func collectOpenAIFunctionCall(call gjson.Result, segments *[]string) {
	if !call.Exists() {
		return
	}
	addIfNotEmpty(segments, call.Get("name").String())
	addIfNotEmpty(segments, call.Get("arguments").String())
}

// collectOpenAITools handles both array and single-tool shapes.
func collectOpenAITools(tools gjson.Result, segments *[]string) {
	if !tools.Exists() {
		return
	}
	if tools.IsArray() {
		tools.ForEach(func(_, tool gjson.Result) bool {
			appendToolPayload(tool, segments)
			return true
		})
		return
	}
	appendToolPayload(tools, segments)
}

// collectOpenAIFunctions handles the legacy functions array.
func collectOpenAIFunctions(functions gjson.Result, segments *[]string) {
	if !functions.Exists() || !functions.IsArray() {
		return
	}
	functions.ForEach(func(_, function gjson.Result) bool {
		addIfNotEmpty(segments, function.Get("name").String())
		addIfNotEmpty(segments, function.Get("description").String())
		if params := function.Get("parameters"); params.Exists() {
			addIfNotEmpty(segments, params.Raw)
		}
		return true
	})
}

// collectOpenAIToolChoice handles both the string form ("auto", "none",
// "required") and the object form (function/tool name selector).
func collectOpenAIToolChoice(choice gjson.Result, segments *[]string) {
	if !choice.Exists() {
		return
	}
	if choice.Type == gjson.String {
		addIfNotEmpty(segments, choice.String())
		return
	}
	addIfNotEmpty(segments, choice.Raw)
}

// collectOpenAIResponseFormat handles both json_schema and plain schema.
func collectOpenAIResponseFormat(format gjson.Result, segments *[]string) {
	if !format.Exists() {
		return
	}
	addIfNotEmpty(segments, format.Get("type").String())
	addIfNotEmpty(segments, format.Get("name").String())
	if schema := format.Get("json_schema"); schema.Exists() {
		addIfNotEmpty(segments, schema.Raw)
	}
	if schema := format.Get("schema"); schema.Exists() {
		addIfNotEmpty(segments, schema.Raw)
	}
}

// appendToolPayload extracts a single tool descriptor (type + optional
// function metadata).
func appendToolPayload(tool gjson.Result, segments *[]string) {
	if !tool.Exists() {
		return
	}
	addIfNotEmpty(segments, tool.Get("type").String())
	addIfNotEmpty(segments, tool.Get("name").String())
	addIfNotEmpty(segments, tool.Get("description").String())
	if function := tool.Get("function"); function.Exists() {
		addIfNotEmpty(segments, function.Get("name").String())
		addIfNotEmpty(segments, function.Get("description").String())
		if params := function.Get("parameters"); params.Exists() {
			addIfNotEmpty(segments, params.Raw)
		}
	}
}

// addIfNotEmpty appends value to segments after trimming, skipping empty
// strings so the joined result has no leading/interior blank lines.
func addIfNotEmpty(segments *[]string, value string) {
	if segments == nil {
		return
	}
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		*segments = append(*segments, trimmed)
	}
}

// intFromString parses a numeric string, defaulting to 0 on any error.
// Not currently used but kept alongside the parsers for future extensions
// where headers or metadata carry decimal counts.
func intFromString(s string) int64 {
	if s == "" {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return n
}
