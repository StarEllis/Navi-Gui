package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// openAIBackend 走 OpenAI 兼容的 Chat Completions 接口（DeepSeek、OpenRouter、大部分中转站都认）。
type openAIBackend struct {
	httpClient *http.Client
	endpoint   string
	apiKey     string
	model      string
	effort     string
	tools      []map[string]any
	history    []map[string]any
}

func newOpenAIBackend(cfg Config) *openAIBackend {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultOpenAIBaseURL
	}
	tools := make([]map[string]any, 0, len(toolSpecs))
	for _, spec := range toolSpecs {
		tools = append(tools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        spec.Name,
				"description": spec.Description,
				"parameters": map[string]any{
					"type":       "object",
					"properties": spec.Properties,
					"required":   spec.Required,
				},
			},
		})
	}
	return &openAIBackend{
		httpClient: &http.Client{Timeout: 5 * time.Minute},
		endpoint:   baseURL + "/chat/completions",
		apiKey:     cfg.APIKey,
		model:      cfg.Model,
		tools:      tools,
	}
}

func (b *openAIBackend) addUser(text string) {
	b.history = append(b.history, map[string]any{"role": "user", "content": text})
}

func (b *openAIBackend) addAssistant(text string) {
	b.history = append(b.history, map[string]any{"role": "assistant", "content": text})
}

func (b *openAIBackend) setEffort(effort string) { b.effort = effort }

type openAIResponse struct {
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content          *string           `json:"content"`
			ReasoningContent string            `json:"reasoning_content"`
			Reasoning        string            `json:"reasoning"` // OpenRouter 等用这个名字
			ToolCalls        []json.RawMessage `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
}

type openAIToolCall struct {
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func (b *openAIBackend) next(ctx context.Context) (roundResult, error) {
	messages := append([]map[string]any{{"role": "system", "content": systemPrompt}}, b.history...)
	request := map[string]any{
		"model":    b.model,
		"messages": messages,
		"tools":    b.tools,
	}
	// 只在用户选了推理强度时才带：不支持推理的模型见到这个字段可能报错。
	if b.effort != "" {
		request["reasoning_effort"] = b.effort
	}
	body, err := json.Marshal(request)
	if err != nil {
		return roundResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint, bytes.NewReader(body))
	if err != nil {
		return roundResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+b.apiKey)

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return roundResult{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return roundResult{}, err
	}
	if resp.StatusCode != http.StatusOK {
		snippet := strings.TrimSpace(string(data))
		if len(snippet) > 500 {
			snippet = snippet[:500] + "…"
		}
		return roundResult{}, fmt.Errorf("接口返回 %d：%s", resp.StatusCode, snippet)
	}

	var parsed openAIResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return roundResult{}, fmt.Errorf("看不懂接口的返回：%w", err)
	}
	if len(parsed.Choices) == 0 {
		return roundResult{}, fmt.Errorf("接口没有返回任何回答")
	}
	choice := parsed.Choices[0]

	// 只回传协议里的字段；reasoning_content 是 DeepSeek 等思考模型的，工具调用过程中要原样带回。
	assistant := map[string]any{"role": "assistant", "content": choice.Message.Content}
	if len(choice.Message.ToolCalls) > 0 {
		assistant["tool_calls"] = choice.Message.ToolCalls
	}
	if choice.Message.ReasoningContent != "" {
		assistant["reasoning_content"] = choice.Message.ReasoningContent
	}
	b.history = append(b.history, assistant)

	var result roundResult
	if choice.Message.Content != nil {
		result.Text = *choice.Message.Content
	}
	result.Reasoning = choice.Message.ReasoningContent
	if result.Reasoning == "" {
		result.Reasoning = choice.Message.Reasoning
	}
	for _, raw := range choice.Message.ToolCalls {
		var call openAIToolCall
		if err := json.Unmarshal(raw, &call); err != nil {
			return roundResult{}, fmt.Errorf("看不懂模型的工具调用：%w", err)
		}
		result.Calls = append(result.Calls, toolCall{
			ID:    call.ID,
			Name:  call.Function.Name,
			Input: json.RawMessage(call.Function.Arguments),
		})
	}

	switch choice.FinishReason {
	case "content_filter":
		result.Stop = stopRefusal
	case "length":
		result.Stop = stopMaxTokens
	case "tool_calls":
		result.Stop = stopToolUse
	default:
		result.Stop = stopDone
	}
	return result, nil
}

func (b *openAIBackend) addToolResults(results []toolResult) {
	for _, r := range results {
		b.history = append(b.history, map[string]any{
			"role":         "tool",
			"tool_call_id": r.CallID,
			"content":      r.Content,
		})
	}
}

func (b *openAIBackend) mark() int { return len(b.history) }

func (b *openAIBackend) rollback(mark int) { b.history = b.history[:mark] }
