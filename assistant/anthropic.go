package assistant

import (
	"context"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// 支持服务端拒答回退（fallbacks: "default"）的模型。只在官方接口上开：
// 中转站不一定认这个 beta，带上反而整句报错。
// 认 adaptive 思考的模型前缀。只给这些模型要思考摘要，别的模型（含中转站上的非 Claude 模型）不带这个参数。
var adaptiveThinkingPrefixes = []string{
	"claude-fable-", "claude-mythos-", "claude-opus-5", "claude-sonnet-5", "claude-haiku-5",
	"claude-opus-4-6", "claude-opus-4-7", "claude-opus-4-8", "claude-sonnet-4-6",
}

func supportsAdaptiveThinking(model string) bool {
	for _, prefix := range adaptiveThinkingPrefixes {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	return false
}

var fallbackModels = map[string]bool{
	"claude-fable-5-1":  true,
	"claude-opus-5-5":   true,
	"claude-opus-5":     true,
	"claude-sonnet-5-5": true,
}

type anthropicBackend struct {
	client   anthropic.Client
	model    string
	fallback bool
	effort   string
	forced   string // 下一轮强制调用的工具，用一次就清掉
	tools    []anthropic.ToolUnionParam
	history  []anthropic.MessageParam
}

func newAnthropicBackend(cfg Config) *anthropicBackend {
	options := []option.RequestOption{option.WithAPIKey(cfg.APIKey)}
	official := cfg.BaseURL == "" || strings.Contains(cfg.BaseURL, "api.anthropic.com")
	if cfg.BaseURL != "" {
		options = append(options, option.WithBaseURL(cfg.BaseURL))
	}

	tools := make([]anthropic.ToolUnionParam, 0, len(toolSpecs))
	for _, spec := range toolSpecs {
		tool := anthropic.ToolParam{
			Name:        spec.Name,
			Description: anthropic.String(spec.Description),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: spec.Properties,
				Required:   spec.Required,
			},
		}
		tools = append(tools, anthropic.ToolUnionParam{OfTool: &tool})
	}

	return &anthropicBackend{
		client:   anthropic.NewClient(options...),
		model:    cfg.Model,
		fallback: official && fallbackModels[cfg.Model],
		tools:    tools,
	}
}

func (b *anthropicBackend) addUser(text string) {
	b.history = append(b.history, anthropic.NewUserMessage(anthropic.NewTextBlock(text)))
}

func (b *anthropicBackend) addAssistant(text string) {
	b.history = append(b.history, anthropic.NewAssistantMessage(anthropic.NewTextBlock(text)))
}

func (b *anthropicBackend) setEffort(effort string) { b.effort = effort }

func (b *anthropicBackend) forceTool(name string) { b.forced = name }

func (b *anthropicBackend) next(ctx context.Context) (roundResult, error) {
	// effort 只在用户选了推理强度时才带，默认时用模型自己的（Claude Opus 5.5 默认 medium），
	// 也免得不认这个参数的模型报错。
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(b.model),
		MaxTokens: 16000,
		System:    []anthropic.TextBlockParam{{Text: systemPrompt}},
		Messages:  b.history,
		Tools:     b.tools,
		// 自动把缓存断点放在最后一个可缓存块上：工具、系统提示和前面的对话都能复用。
		CacheControl: anthropic.NewCacheControlEphemeralParam(),
	}
	// 默认不返回思考内容，要显式要摘要，界面才有"思考过程"可看。
	if supportsAdaptiveThinking(b.model) {
		params.Thinking = anthropic.ThinkingConfigParamUnion{OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{
			Display: anthropic.ThinkingConfigAdaptiveDisplaySummarized,
		}}
	} else if b.forced != "" {
		// 开着思考时接口不允许指定工具，只能靠追加的那句提示。
		params.ToolChoice = anthropic.ToolChoiceParamOfTool(b.forced)
	}
	b.forced = ""
	if b.effort != "" {
		params.OutputConfig = anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffort(b.effort)}
	}
	var options []option.RequestOption
	if b.fallback {
		options = append(options,
			option.WithHeaderAdd("anthropic-beta", "server-side-fallback-2026-07-01"),
			option.WithJSONSet("fallbacks", "default"),
		)
	}

	resp, err := b.client.Messages.New(ctx, params, options...)
	if err != nil {
		return roundResult{}, err
	}
	// 原样追加整条回复（含 thinking 块），历史只追加不改写。
	b.history = append(b.history, resp.ToParam())

	var result roundResult
	var texts, thoughts []string
	for _, block := range resp.Content {
		switch variant := block.AsAny().(type) {
		case anthropic.ThinkingBlock:
			if strings.TrimSpace(variant.Thinking) != "" {
				thoughts = append(thoughts, variant.Thinking)
			}
		case anthropic.TextBlock:
			texts = append(texts, variant.Text)
		case anthropic.ToolUseBlock:
			result.Calls = append(result.Calls, toolCall{
				ID:    variant.ID,
				Name:  variant.Name,
				Input: []byte(variant.JSON.Input.Raw()),
			})
		}
	}
	result.Text = strings.Join(texts, "\n")
	result.Reasoning = strings.Join(thoughts, "\n\n")

	switch resp.StopReason {
	case anthropic.StopReasonRefusal:
		result.Stop = stopRefusal
	case anthropic.StopReasonMaxTokens:
		result.Stop = stopMaxTokens
	case anthropic.StopReasonToolUse:
		result.Stop = stopToolUse
	default:
		result.Stop = stopDone
	}
	return result, nil
}

func (b *anthropicBackend) addToolResults(results []toolResult) {
	// 同一轮的所有结果放进一条 user 消息。
	blocks := make([]anthropic.ContentBlockParamUnion, 0, len(results))
	for _, r := range results {
		blocks = append(blocks, anthropic.NewToolResultBlock(r.CallID, r.Content, r.IsError))
	}
	b.history = append(b.history, anthropic.NewUserMessage(blocks...))
}

func (b *anthropicBackend) mark() int { return len(b.history) }

func (b *anthropicBackend) rollback(mark int) { b.history = b.history[:mark] }
