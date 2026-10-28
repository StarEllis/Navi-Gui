package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// ListModels 向接口要可用的模型名。只需要类型、地址和 Key。
func ListModels(ctx context.Context, cfg Config) ([]string, error) {
	cfg = cfg.normalized()
	if cfg.APIKey == "" {
		return nil, errors.New("先填 API Key 再拉取模型")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var ids []string
	if cfg.APIType == APITypeOpenAI {
		baseURL := cfg.BaseURL
		if baseURL == "" {
			baseURL = defaultOpenAIBaseURL
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/models", nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			snippet := strings.TrimSpace(string(data))
			if len(snippet) > 300 {
				snippet = snippet[:300] + "…"
			}
			return nil, fmt.Errorf("接口返回 %d：%s", resp.StatusCode, snippet)
		}
		var parsed struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &parsed); err != nil {
			return nil, fmt.Errorf("看不懂接口返回的模型列表：%w", err)
		}
		for _, model := range parsed.Data {
			ids = append(ids, model.ID)
		}
	} else {
		options := []option.RequestOption{option.WithAPIKey(cfg.APIKey)}
		if cfg.BaseURL != "" {
			options = append(options, option.WithBaseURL(cfg.BaseURL))
		}
		client := anthropic.NewClient(options...)
		pager := client.Models.ListAutoPaging(ctx, anthropic.ModelListParams{})
		for pager.Next() {
			ids = append(ids, pager.Current().ID)
		}
		if err := pager.Err(); err != nil {
			return nil, err
		}
	}

	seen := map[string]bool{}
	models := make([]string, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" && !seen[id] {
			seen[id] = true
			models = append(models, id)
		}
	}
	if len(models) == 0 {
		return nil, errors.New("接口没有返回任何模型")
	}
	sort.Strings(models)
	return models, nil
}
