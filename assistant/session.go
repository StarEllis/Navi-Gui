package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// 一句话里最多来回这么多轮。正常找片两三轮就够，超过多半是模型在原地打转。
const maxRounds = 12

const (
	stopDone      = "done"
	stopToolUse   = "tool_use"
	stopRefusal   = "refusal"
	stopMaxTokens = "max_tokens"
)

type roundResult struct {
	Text      string
	Reasoning string // 模型的思考内容（DeepSeek 的 reasoning_content、Claude 的思考摘要），没有就是空
	Calls     []toolCall
	Stop      string
}

// backend 是一种接口协议的对话历史加一次请求。历史只追加；
// 一轮失败就截回这轮开始前的长度，等于这句话没发过。
type backend interface {
	addUser(text string)
	addAssistant(text string)
	setEffort(effort string)
	next(ctx context.Context) (roundResult, error)
	addToolResults(results []toolResult)
	mark() int
	rollback(mark int)
}

var errBusy = errors.New("上一句还没处理完")

// Session 管着当前打开的会话。同一时间只处理一句话。
type Session struct {
	sandbox *Sandbox
	userID  string
	dir     string // 会话文件所在目录
	// OnStep 在每次工具调用结束后触发，界面据此实时显示进度。
	OnStep func(Step)

	mu         sync.Mutex
	busy       bool
	cancel     context.CancelFunc
	conv       *Conversation
	backend    backend
	backendKey string
}

func NewSession(sandbox *Sandbox, userID, conversationDir string) *Session {
	return &Session{sandbox: sandbox, userID: userID, dir: conversationDir}
}

// Send 把一句话发给模型，结果（包括出错）都记进当前会话并存盘，返回整段会话。
// 只有"正忙"和"一句话都没说"这种没进会话的情况才返回 error。
func (s *Session) Send(ctx context.Context, cfg Config, text string) (*Conversation, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("说点什么再发送")
	}
	cfg = cfg.normalized()

	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		return nil, errBusy
	}
	if s.conv == nil {
		s.conv = newConversation(text)
	}
	conv := s.conv
	// 换了接口、地址、模型或 Key：新开一份历史，把之前的问答当纯文本补进去。
	key := strings.Join([]string{cfg.APIType, cfg.BaseURL, cfg.Model, cfg.APIKey}, "\x00")
	if s.backend == nil || s.backendKey != key {
		s.backend = newBackend(cfg)
		s.backendKey = key
		seedHistory(s.backend, conv.Items)
	}
	b := s.backend
	// 推理强度每句都可以换，不影响已有的对话历史。
	b.setEffort(cfg.Effort)
	ctx, cancel := context.WithCancel(ctx)
	s.busy = true
	s.cancel = cancel
	s.mu.Unlock()

	defer func() {
		cancel()
		s.mu.Lock()
		s.busy = false
		s.cancel = nil
		s.mu.Unlock()
	}()

	conv.Items = append(conv.Items, ChatItem{Role: "user", Text: text})
	var reply ChatItem
	if err := cfg.validate(); err != nil {
		reply = ChatItem{Role: "error", Text: err.Error()}
	} else {
		reply = s.runTurn(ctx, b, text)
	}
	conv.Items = append(conv.Items, reply)
	if err := saveConversation(s.dir, conv); err != nil {
		conv.Items = append(conv.Items, ChatItem{Role: "error", Text: "会话没存上：" + err.Error()})
	}
	return copyConversation(conv), nil
}

func (s *Session) runTurn(ctx context.Context, b backend, text string) ChatItem {
	fail := func(message string) ChatItem { return ChatItem{Role: "error", Text: message} }

	mark := b.mark()
	b.addUser(text)
	turn := &turnState{steps: []Step{}, media: []MediaCard{}, shown: map[string]bool{}}
	for round := 0; round < maxRounds; round++ {
		result, err := b.next(ctx)
		if err != nil {
			b.rollback(mark)
			if ctx.Err() == context.Canceled {
				return fail("已停止")
			}
			return fail(err.Error())
		}
		// 思考内容和工具调用之间顺口说的话都记成"思考"步骤，界面按时间线显示。
		thought := strings.TrimSpace(result.Reasoning)
		if len(result.Calls) > 0 && strings.TrimSpace(result.Text) != "" {
			thought = strings.TrimSpace(thought + "\n\n" + strings.TrimSpace(result.Text))
		}
		if thought != "" {
			s.recordStep(turn, Step{Tool: stepThinking, Input: thought})
		}

		switch {
		case result.Stop == stopRefusal:
			b.rollback(mark)
			return fail("模型拒绝回答这句话，换个说法试试，或者换一个模型")
		case result.Stop == stopMaxTokens && len(result.Calls) > 0:
			b.rollback(mark)
			return fail("模型的回复太长被截断了，换个问法试试")
		case len(result.Calls) == 0:
			answer := strings.TrimSpace(result.Text)
			if result.Stop == stopMaxTokens {
				answer += "\n\n（回答太长，后面被截断了）"
			}
			return ChatItem{Role: "assistant", Text: answer, Steps: turn.steps, Media: turn.media}
		}

		results := make([]toolResult, 0, len(result.Calls))
		for _, call := range result.Calls {
			results = append(results, s.runTool(ctx, call, turn))
		}
		b.addToolResults(results)
	}
	b.rollback(mark)
	return fail(fmt.Sprintf("查了 %d 轮还没得出结论，换个更具体的说法试试", maxRounds))
}

// Stop 中止正在处理的那句话。
func (s *Session) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
}

// New 关掉当前会话，下一句开一个新的。
func (s *Session) New() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy {
		return errBusy
	}
	s.conv, s.backend, s.backendKey = nil, nil, ""
	return nil
}

// Open 切到一个存过的会话，卡片上的收藏、评分按现在的状态重新取。
func (s *Session) Open(ctx context.Context, id string) (*Conversation, error) {
	conv, err := loadConversation(s.dir, id)
	if err != nil {
		return nil, err
	}
	for i := range conv.Items {
		item := &conv.Items[i]
		if len(item.Media) == 0 {
			continue
		}
		ids := make([]string, 0, len(item.Media))
		for _, card := range item.Media {
			ids = append(ids, card.ID)
		}
		if fresh, err := s.sandbox.loadMediaCards(ctx, ids, s.userID); err == nil {
			item.Media = fresh // 已删掉的片子顺带消失
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy {
		return nil, errBusy
	}
	s.conv, s.backend, s.backendKey = conv, nil, ""
	return copyConversation(conv), nil
}

// Current 返回当前打开的会话，没有就是 nil。
func (s *Session) Current() *Conversation {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conv == nil {
		return nil
	}
	return copyConversation(s.conv)
}

func (s *Session) List() ([]ConversationSummary, error) {
	return listConversations(s.dir)
}

func (s *Session) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conv != nil && s.conv.ID == id {
		if s.busy {
			return errBusy
		}
		s.conv, s.backend, s.backendKey = nil, nil, ""
	}
	return deleteConversation(s.dir, id)
}

func copyConversation(conv *Conversation) *Conversation {
	c := *conv
	c.Items = append([]ChatItem(nil), conv.Items...)
	return &c
}

func newBackend(cfg Config) backend {
	if cfg.APIType == APITypeOpenAI {
		return newOpenAIBackend(cfg)
	}
	return newAnthropicBackend(cfg)
}
