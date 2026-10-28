package assistant

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// ChatItem 是界面上的一条消息。会话文件存的就是这些，重开会话时也靠它们给模型补上下文。
type ChatItem struct {
	Role  string      `json:"role"` // user | assistant | error
	Text  string      `json:"text"`
	Steps []Step      `json:"steps,omitempty"`
	Media []MediaCard `json:"media,omitempty"`
}

type Conversation struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	UpdatedAt int64      `json:"updated_at"` // Unix 毫秒
	Items     []ChatItem `json:"items"`
}

type ConversationSummary struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	UpdatedAt int64  `json:"updated_at"`
}

const maxTitleRunes = 24

func newConversation(firstMessage string) *Conversation {
	suffix := make([]byte, 3)
	_, _ = rand.Read(suffix)
	title := strings.Join(strings.Fields(firstMessage), " ")
	if utf8.RuneCountInString(title) > maxTitleRunes {
		title = string([]rune(title)[:maxTitleRunes]) + "…"
	}
	return &Conversation{
		ID:    time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(suffix),
		Title: title,
		Items: []ChatItem{},
	}
}

func conversationPath(dir, id string) (string, error) {
	// id 只会是我们自己生成的那种格式，挡掉 ../ 之类的路径。
	if id == "" || strings.ContainsAny(id, `/\.:`) {
		return "", fmt.Errorf("会话编号不对：%q", id)
	}
	return filepath.Join(dir, id+".json"), nil
}

func saveConversation(dir string, conv *Conversation) error {
	path, err := conversationPath(dir, conv.ID)
	if err != nil {
		return err
	}
	conv.UpdatedAt = time.Now().UnixMilli()
	data, err := json.MarshalIndent(conv, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func loadConversation(dir, id string) (*Conversation, error) {
	path, err := conversationPath(dir, id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("这个会话已经不存在了")
	}
	if err != nil {
		return nil, err
	}
	var conv Conversation
	if err := json.Unmarshal(data, &conv); err != nil {
		return nil, fmt.Errorf("会话文件坏了：%w", err)
	}
	if conv.Items == nil {
		conv.Items = []ChatItem{}
	}
	return &conv, nil
}

func listConversations(dir string) ([]ConversationSummary, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []ConversationSummary{}, nil
	}
	if err != nil {
		return nil, err
	}
	summaries := []ConversationSummary{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		conv, err := loadConversation(dir, strings.TrimSuffix(name, ".json"))
		if err != nil {
			continue // 坏文件不影响列出其他会话
		}
		summaries = append(summaries, ConversationSummary{ID: conv.ID, Title: conv.Title, UpdatedAt: conv.UpdatedAt})
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].UpdatedAt > summaries[j].UpdatedAt })
	return summaries, nil
}

func deleteConversation(dir, id string) error {
	path, err := conversationPath(dir, id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// seedHistory 把已有的问答对按纯文本喂回模型。工具调用细节不回放，
// 但把展示过的影片 id 附在回答后面，"第三部那种"之类的追问才接得上。
func seedHistory(b backend, items []ChatItem) {
	for i := 0; i+1 < len(items); i++ {
		if items[i].Role != "user" || items[i+1].Role != "assistant" {
			continue
		}
		answer := items[i+1].Text
		if len(items[i+1].Media) > 0 {
			ids := make([]string, 0, len(items[i+1].Media))
			for _, card := range items[i+1].Media {
				ids = append(ids, card.ID+"（"+card.Code+"）")
			}
			answer += "\n\n[当时展示的影片，按顺序：" + strings.Join(ids, "、") + "]"
		}
		b.addUser(items[i].Text)
		b.addAssistant(answer)
		i++
	}
}
