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
	// Changes 是这一轮提出过的修改和结果。
	Changes []Proposal `json:"changes,omitempty"`
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

// seedHistory 把已有的问答对按纯文本喂回模型，规则见 replayTurn。
func seedHistory(b backend, items []ChatItem) {
	for i := 0; i+1 < len(items); i++ {
		if items[i].Role != "user" || items[i+1].Role == "user" {
			continue
		}
		replayTurn(b, questionFor(items[i]), items[i+1])
		i++
	}
}

// questionFor 是发给模型的那句话。在详情页上问的，后面注明是哪部片。
func questionFor(item ChatItem) string {
	if len(item.Media) == 0 {
		return item.Text
	}
	card := item.Media[0]
	return item.Text + "\n\n（用户是在这部影片的详情页上问的，「这部」「这个演员」之类指的就是它：id " +
		card.ID + "，番号 " + card.Code + "，标题 " + card.Title + "）"
}

// replayTurn 把一问一答按纯文本补进历史。工具调用细节不回放，
// 但把展示过的影片 id 附在回答后面，"第三部那种"之类的追问才接得上。
// 出错的那句一般等于没发过；但出错前已经改过东西的，得让模型知道，免得重复去改。
func replayTurn(b backend, question string, reply ChatItem) {
	var answer string
	switch {
	case reply.Role == "assistant":
		answer = reply.Text
		if len(reply.Media) > 0 {
			ids := make([]string, 0, len(reply.Media))
			for _, card := range reply.Media {
				ids = append(ids, card.ID+"（"+card.Code+"）")
			}
			answer += "\n\n[当时展示的影片，按顺序：" + strings.Join(ids, "、") + "]"
		}
		if len(reply.Changes) > 0 {
			answer += "\n\n[这一轮提出的修改：" + describeChanges(reply.Changes) + "]"
		}
	case reply.Role == "error" && len(reply.Changes) > 0:
		// 出错消息里已经写了处理过的修改。
		answer = "[这句话没处理完就出错了：" + reply.Text + "]"
	default:
		return
	}
	b.addUser(question)
	b.addAssistant(answer)
}

// describeChanges 把修改和结果压成一行，回放历史和出错提示都用它。
func describeChanges(changes []Proposal) string {
	parts := make([]string, 0, len(changes))
	for _, change := range changes {
		status := map[string]string{
			ProposalApplied:   "已执行",
			ProposalCancelled: "用户没同意",
			ProposalFailed:    "执行出错",
		}[change.Status]
		parts = append(parts, change.Title+"（"+status+"："+change.Result+"）")
	}
	return strings.Join(parts, "；")
}
