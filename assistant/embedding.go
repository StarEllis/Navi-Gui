package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

const (
	DefaultEmbeddingBaseURL = "https://api.siliconflow.cn/v1"
	DefaultEmbeddingModel   = "BAAI/bge-m3"
)

// EmbeddingConfig 是"按意思找片"用的向量模型接口，跟聊天的预设分开配。
type EmbeddingConfig struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
	Model   string `json:"model"`
}

func (c EmbeddingConfig) normalized() EmbeddingConfig {
	c.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	c.APIKey = strings.TrimSpace(c.APIKey)
	c.Model = strings.TrimSpace(c.Model)
	return c
}

// withDefaults 补上没填的地址和模型名，只用于发请求，不写回设置。
func (c EmbeddingConfig) withDefaults() EmbeddingConfig {
	c = c.normalized()
	if c.BaseURL == "" {
		c.BaseURL = DefaultEmbeddingBaseURL
	}
	if c.Model == "" {
		c.Model = DefaultEmbeddingModel
	}
	return c
}

// Enabled 表示填了 Key，可以用。
func (c EmbeddingConfig) Enabled() bool { return c.normalized().APIKey != "" }

var embeddingHTTP = &http.Client{Timeout: 2 * time.Minute}

// embedTexts 把一批文字换成向量（已归一化，比较时直接点乘）。顺序和传入一致。
func embedTexts(ctx context.Context, cfg EmbeddingConfig, texts []string) ([][]float32, error) {
	cfg = cfg.withDefaults()
	if cfg.APIKey == "" {
		return nil, errors.New("还没填向量/嵌入模型的 API Key")
	}
	body, err := json.Marshal(map[string]any{"model": cfg.Model, "input": texts})
	if err != nil {
		return nil, err
	}
	var data []byte
	for attempt := 0; ; attempt++ {
		var retryable bool
		data, retryable, err = postEmbedding(ctx, cfg, body)
		if err == nil || !retryable || attempt >= 2 || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 2 * time.Second):
		}
	}
	if err != nil {
		return nil, err
	}

	var parsed struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("看不懂向量接口的返回：%w", err)
	}
	if len(parsed.Data) != len(texts) {
		return nil, fmt.Errorf("向量接口返回了 %d 条，应该是 %d 条", len(parsed.Data), len(texts))
	}
	vectors := make([][]float32, len(texts))
	for _, item := range parsed.Data {
		if item.Index < 0 || item.Index >= len(texts) || len(item.Embedding) == 0 {
			return nil, errors.New("向量接口返回的数据不完整")
		}
		vectors[item.Index] = normalize(item.Embedding)
	}
	return vectors, nil
}

func postEmbedding(ctx context.Context, cfg EmbeddingConfig, body []byte) ([]byte, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.BaseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	resp, err := embeddingHTTP.Do(req)
	if err != nil {
		return nil, true, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, true, err
	}
	if resp.StatusCode != http.StatusOK {
		snippet := strings.TrimSpace(string(data))
		if len(snippet) > 300 {
			snippet = snippet[:300] + "…"
		}
		retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return nil, retryable, fmt.Errorf("向量接口返回 %d：%s", resp.StatusCode, snippet)
	}
	return data, false, nil
}

func normalize(values []float64) []float32 {
	var sum float64
	for _, v := range values {
		sum += v * v
	}
	norm := math.Sqrt(sum)
	if norm == 0 {
		norm = 1
	}
	out := make([]float32, len(values))
	for i, v := range values {
		out[i] = float32(v / norm)
	}
	return out
}

func dot(a, b []float32) float32 {
	var sum float32
	for i := range a {
		sum += a[i] * b[i]
	}
	return sum
}

// TestEmbedding 发一句话试试接口通不通，返回向量的长度（维数）。
func TestEmbedding(ctx context.Context, cfg EmbeddingConfig) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	vectors, err := embedTexts(ctx, cfg, []string{"测试"})
	if err != nil {
		return 0, err
	}
	return len(vectors[0]), nil
}
