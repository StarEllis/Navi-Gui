package assistant

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	APITypeAnthropic = "anthropic"
	APITypeOpenAI    = "openai"

	defaultAnthropicModel = "claude-opus-5-5"
	defaultOpenAIBaseURL  = "https://api.openai.com/v1"

	// 助手的三档权限。
	PermissionReadOnly = "readonly" // 只能查
	PermissionEdit     = "edit"     // 能改，每次都要用户确认
	PermissionFull     = "full"     // 小改动直接做，还能删视频文件
)

var permissionLabels = map[string]string{
	PermissionReadOnly: "仅可查看",
	PermissionEdit:     "可以修改",
	PermissionFull:     "完全权限",
}

// Config 是真正发请求要用的那一组：接口类型、地址、Key、模型。
type Config struct {
	APIType string `json:"api_type"`
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
	Model   string `json:"model"`
	// Effort 是推理强度：low / medium / high，空表示不传、用模型默认。
	Effort string `json:"effort"`
	// Permission 是 readonly / edit / full，空当 edit。
	Permission string `json:"permission"`
}

// Preset 是一套命名的接口配置，下面挂着拉到或手填的模型。
type Preset struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	APIType string   `json:"api_type"`
	BaseURL string   `json:"base_url"`
	APIKey  string   `json:"api_key"`
	Models  []string `json:"models"`
}

// Settings 是 data/assistant.json 的全部内容。Key 和其他设置一样明文存在 data 目录。
type Settings struct {
	Presets      []Preset `json:"presets"`
	ActivePreset string   `json:"active_preset"`
	ActiveModel  string   `json:"active_model"`
	Effort       string   `json:"effort"`
	Permission   string   `json:"permission"`
}

func LoadSettings(path string) (Settings, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Settings{Presets: []Preset{}}, nil
	}
	if err != nil {
		return Settings{Presets: []Preset{}}, err
	}
	var raw struct {
		Settings
		// 旧版只有一组配置，读到时转成一个预设。
		APIType string   `json:"api_type"`
		BaseURL string   `json:"base_url"`
		APIKey  string   `json:"api_key"`
		Model   string   `json:"model"`
		Models  []string `json:"models"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Settings{Presets: []Preset{}}, fmt.Errorf("read assistant settings: %w", err)
	}
	settings := raw.Settings
	if len(settings.Presets) == 0 && (raw.APIKey != "" || raw.BaseURL != "") {
		models := raw.Models
		if raw.Model != "" && !contains(models, raw.Model) {
			models = append([]string{raw.Model}, models...)
		}
		settings.Presets = []Preset{{
			ID: "default", Name: "默认", APIType: raw.APIType,
			BaseURL: raw.BaseURL, APIKey: raw.APIKey, Models: models,
		}}
		settings.ActivePreset = "default"
		settings.ActiveModel = raw.Model
	}
	return settings.normalized(), nil
}

func SaveSettings(path string, settings Settings) error {
	settings = settings.normalized()
	seen := map[string]bool{}
	for _, preset := range settings.Presets {
		if preset.ID == "" || seen[preset.ID] {
			return fmt.Errorf("预设 %q 的编号为空或重复", preset.Name)
		}
		seen[preset.ID] = true
		if preset.Name == "" {
			return errors.New("预设名称不能为空")
		}
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func (s Settings) normalized() Settings {
	if s.Presets == nil {
		s.Presets = []Preset{}
	}
	switch s.Effort {
	case "", "low", "medium", "high":
	default:
		s.Effort = ""
	}
	s.Permission = normalizePermission(s.Permission)
	for i, p := range s.Presets {
		p.ID = strings.TrimSpace(p.ID)
		p.Name = strings.TrimSpace(p.Name)
		p.APIType = strings.TrimSpace(p.APIType)
		if p.APIType == "" {
			p.APIType = APITypeAnthropic
		}
		p.BaseURL = strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
		p.APIKey = strings.TrimSpace(p.APIKey)
		models := make([]string, 0, len(p.Models))
		for _, model := range p.Models {
			if model = strings.TrimSpace(model); model != "" && !contains(models, model) {
				models = append(models, model)
			}
		}
		p.Models = models
		s.Presets[i] = p
	}
	// 当前选中的预设或模型被删掉了，就退回第一个能用的。
	active := s.preset(s.ActivePreset)
	if active == nil && len(s.Presets) > 0 {
		active = &s.Presets[0]
	}
	if active == nil {
		s.ActivePreset, s.ActiveModel = "", ""
		return s
	}
	s.ActivePreset = active.ID
	if !contains(active.Models, s.ActiveModel) {
		s.ActiveModel = ""
		if len(active.Models) > 0 {
			s.ActiveModel = active.Models[0]
		} else if active.APIType == APITypeAnthropic {
			s.ActiveModel = defaultAnthropicModel
		}
	}
	return s
}

func (s Settings) preset(id string) *Preset {
	for i := range s.Presets {
		if s.Presets[i].ID == id {
			return &s.Presets[i]
		}
	}
	return nil
}

// Active 返回当前选中的预设加模型，拿去发请求。
func (s Settings) Active() (Config, error) {
	s = s.normalized()
	preset := s.preset(s.ActivePreset)
	if preset == nil {
		return Config{}, errors.New("还没有接口预设，先到助手设置里新建一个")
	}
	cfg := Config{APIType: preset.APIType, BaseURL: preset.BaseURL, APIKey: preset.APIKey, Model: s.ActiveModel, Effort: s.Effort, Permission: s.Permission}
	return cfg, cfg.validate()
}

func (c Config) normalized() Config {
	c.APIType = strings.TrimSpace(c.APIType)
	c.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	c.APIKey = strings.TrimSpace(c.APIKey)
	c.Model = strings.TrimSpace(c.Model)
	return c
}

func (c Config) validate() error {
	switch c.APIType {
	case APITypeAnthropic, APITypeOpenAI:
	default:
		return fmt.Errorf("不认识的接口类型：%q", c.APIType)
	}
	if c.APIKey == "" {
		return errors.New("这个预设还没填 API Key")
	}
	if c.Model == "" {
		return errors.New("这个预设下还没有模型，先拉取或手动添加一个")
	}
	return nil
}

func normalizePermission(permission string) string {
	if _, ok := permissionLabels[permission]; ok {
		return permission
	}
	return PermissionEdit
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
