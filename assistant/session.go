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

// backend 是一种接口协议的对话历史加一次请求。一句话处理中历史只追加；
// 处理完就截回这句话开始前的长度，再把问答压成纯文本补回去（见 runTurn）。
type backend interface {
	addUser(text string)
	addAssistant(text string)
	setEffort(effort string)
	// forceTool 让下一轮请求必须调用这个工具，只管一轮。
	forceTool(name string)
	next(ctx context.Context) (roundResult, error)
	addToolResults(results []toolResult)
	mark() int
	rollback(mark int)
}

// showMediaNudge 在模型查到片子却忘了展示时追加一句，让它补调 show_media。
const showMediaNudge = "（系统提示：你刚才的回答提到了具体影片，但没有调用 show_media，用户看不到卡片。请调用 show_media 展示你回答里提到的那些影片；如果回答其实不涉及具体影片，回复「无」即可。）"

var errBusy = errors.New("上一句还没处理完")

// Session 管着当前打开的会话。同一时间只处理一句话。
type Session struct {
	sandbox *Sandbox
	userID  string
	dir     string // 会话文件所在目录
	// OnStep 在每次工具调用结束后触发，界面据此实时显示进度。
	OnStep func(Step)
	// Executor 负责真正改数据；为 nil 时助手只能查。
	Executor Executor
	// OnConfirm 在要用户确认修改时触发，界面据此弹出确认卡片。
	OnConfirm func(Proposal)

	mu         sync.Mutex
	busy       bool
	cancel     context.CancelFunc
	conv       *Conversation
	backend    backend
	backendKey string
	pending    *Proposal
	decision   chan bool
}

func NewSession(sandbox *Sandbox, userID, conversationDir string) *Session {
	return &Session{sandbox: sandbox, userID: userID, dir: conversationDir}
}

// Send 把一句话发给模型，结果（包括出错）都记进当前会话并存盘，返回整段会话。
// 只有"正忙"和"一句话都没说"这种没进会话的情况才返回 error。
func (s *Session) Send(ctx context.Context, cfg Config, text string) (*Conversation, error) {
	return s.SendAbout(ctx, cfg, text, "")
}

// SendAbout 和 Send 一样，只是这句话是在某部片的详情页上问的：
// 这部片的卡片挂在这句话上，模型也知道"这部"指的是哪部。mediaID 为空就是普通的一句话。
func (s *Session) SendAbout(ctx context.Context, cfg Config, text, mediaID string) (*Conversation, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("说点什么再发送")
	}
	cfg = cfg.normalized()
	question := ChatItem{Role: "user", Text: text}
	if mediaID = strings.TrimSpace(mediaID); mediaID != "" {
		cards, err := s.sandbox.loadMediaCards(ctx, []string{mediaID}, s.userID)
		if err != nil {
			return nil, err
		}
		if len(cards) == 0 {
			return nil, errors.New("这部片已经不在库里了")
		}
		question.Media = cards
	}

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

	// 改 conv.Items 都在锁里做，Current 同时来读也不会读到一半。
	s.mu.Lock()
	conv.Items = append(conv.Items, question)
	s.mu.Unlock()
	var reply ChatItem
	if err := cfg.validate(); err != nil {
		reply = ChatItem{Role: "error", Text: err.Error()}
	} else {
		reply = s.runTurn(ctx, b, questionFor(question), normalizePermission(cfg.Permission))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	conv.Items = append(conv.Items, reply)
	if err := saveConversation(s.dir, conv); err != nil {
		conv.Items = append(conv.Items, ChatItem{Role: "error", Text: "会话没存上：" + err.Error()})
	}
	return copyConversation(conv), nil
}

// runTurn 处理完一句话后，把这句话里的工具调用、查询结果、补卡片的催促都截掉，
// 只留问答的纯文本（和重开会话时一样）：历史不会被查询结果越撑越大，
// 中途出错但已经改过东西时，模型也记得改过什么。
func (s *Session) runTurn(ctx context.Context, b backend, text, permission string) ChatItem {
	mark := b.mark()
	item := s.runRounds(ctx, b, text, permission)
	b.rollback(mark)
	replayTurn(b, text, item)
	return item
}

func (s *Session) runRounds(ctx context.Context, b backend, text, permission string) ChatItem {
	turn := &turnState{steps: []Step{}, media: []MediaCard{}, shown: map[string]bool{}, permission: permission}
	// 这句话中途失败，但前面已经改过东西了：得在出错消息里说清楚，不能当没发生。
	fail := func(message string) ChatItem {
		item := ChatItem{Role: "error", Text: message, Changes: turn.changes}
		if len(turn.changes) > 0 {
			item.Text += "\n\n这句话里已经处理过的修改：" + describeChanges(turn.changes)
		}
		return item
	}

	// 权限每句都可能换，标在这句话后面；系统提示保持不变才能命中缓存。
	b.addUser(text + "\n\n（当前权限：" + permissionLabels[permission] + "）")
	pending := ""
	for round := 0; round < maxRounds; round++ {
		result, err := b.next(ctx)
		if err != nil && pending != "" && ctx.Err() == nil {
			// 只是补卡片那一轮出错：回答已经有了，不能因此整句作废。
			return ChatItem{Role: "assistant", Text: pending, Steps: turn.steps, Media: turn.media, Changes: turn.changes}
		}
		if err != nil {
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
			return fail("模型拒绝回答这句话，换个说法试试，或者换一个模型")
		case result.Stop == stopMaxTokens && len(result.Calls) > 0:
			return fail("模型的回复太长被截断了，换个问法试试")
		case len(result.Calls) == 0:
			answer := strings.TrimSpace(result.Text)
			if result.Stop == stopMaxTokens {
				answer += "\n\n（回答太长，后面被截断了）"
			}
			if pending != "" {
				answer = pending // 催过一次：用第一次的回答，这一轮只为补卡片
			}
			if len(turn.media) == 0 {
				s.showMediaFromAnswer(ctx, answer, turn)
			}
			// 查出了片子却既没展示、回答里也没番号：催模型补调一次 show_media，只催一次。
			if len(turn.media) == 0 && turn.queriedMedia && pending == "" {
				pending = answer
				b.addUser(showMediaNudge)
				// 光提醒不够：有的模型只在文字里说「卡片在下面」却不调工具，这一轮直接强制它调。
				b.forceTool("show_media")
				continue
			}
			return ChatItem{Role: "assistant", Text: answer, Steps: turn.steps, Media: turn.media, Changes: turn.changes}
		}

		results := make([]toolResult, 0, len(result.Calls))
		for _, call := range result.Calls {
			results = append(results, s.runTool(ctx, call, turn))
		}
		b.addToolResults(results)
	}
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
