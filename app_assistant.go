package main

import (
	"errors"

	"navi-desktop/assistant"
	"navi-desktop/config"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	assistantStepEvent    = "assistant-step"
	assistantConfirmEvent = "assistant-confirm"
)

var (
	assistantSettingsPath     = config.DataPath("assistant.json")
	assistantConversationsDir = config.DataPath("assistant", "conversations")
)

func (a *App) GetAssistantSettings() (*assistant.Settings, error) {
	settings, err := assistant.LoadSettings(assistantSettingsPath)
	return &settings, err
}

// SaveAssistantSettings 整体保存：预设的增删改、当前选中的预设和模型都走这里。
func (a *App) SaveAssistantSettings(settings *assistant.Settings) (*assistant.Settings, error) {
	if settings == nil {
		return nil, errors.New("设置为空")
	}
	if err := assistant.SaveSettings(assistantSettingsPath, *settings); err != nil {
		return nil, err
	}
	return a.GetAssistantSettings()
}

// ListAssistantModels 用编辑中的接口类型、地址和 Key 拉模型列表（不需要先保存）。
func (a *App) ListAssistantModels(cfg *assistant.Config) ([]string, error) {
	if cfg == nil {
		return nil, errors.New("设置为空")
	}
	return assistant.ListModels(a.ctx, *cfg)
}

// AssistantSend 把一句话交给助手，等它查完再返回整段会话。查询过程通过 assistant-step 事件实时推给前端。
// mediaID 不为空表示这句话是从这部片的详情页问的。
func (a *App) AssistantSend(text, mediaID string) (*assistant.Conversation, error) {
	session, err := a.assistantSession()
	if err != nil {
		return nil, err
	}
	settings, err := assistant.LoadSettings(assistantSettingsPath)
	if err != nil {
		return nil, err
	}
	cfg, _ := settings.Active() // 配置不完整时由 Send 记成一条出错消息
	return session.SendAbout(a.ctx, cfg, text, mediaID)
}

func (a *App) AssistantStop() {
	if session := a.currentAssistantSession(); session != nil {
		session.Stop()
	}
}

// AssistantDecide 是确认卡片上的"执行"（approve=true）和"取消"。
func (a *App) AssistantDecide(proposalID string, approve bool) error {
	session := a.currentAssistantSession()
	if session == nil {
		return errors.New("这张确认卡片已经失效了")
	}
	return session.Decide(proposalID, approve)
}

// AssistantPendingProposal 返回正在等确认的卡片，没有就是 null。
func (a *App) AssistantPendingProposal() *assistant.Proposal {
	if session := a.currentAssistantSession(); session != nil {
		return session.Pending()
	}
	return nil
}

// AssistantCurrentConversation 返回当前打开的会话，没有就是 null。
func (a *App) AssistantCurrentConversation() *assistant.Conversation {
	if session := a.currentAssistantSession(); session != nil {
		return session.Current()
	}
	return nil
}

func (a *App) AssistantNewConversation() error {
	if session := a.currentAssistantSession(); session != nil {
		return session.New()
	}
	return nil
}

func (a *App) AssistantListConversations() ([]assistant.ConversationSummary, error) {
	session, err := a.assistantSession()
	if err != nil {
		return nil, err
	}
	return session.List()
}

func (a *App) AssistantOpenConversation(id string) (*assistant.Conversation, error) {
	session, err := a.assistantSession()
	if err != nil {
		return nil, err
	}
	return session.Open(a.ctx, id)
}

func (a *App) AssistantDeleteConversation(id string) error {
	session, err := a.assistantSession()
	if err != nil {
		return err
	}
	return session.Delete(id)
}

func (a *App) currentAssistantSession() *assistant.Session {
	a.assistantMu.Lock()
	defer a.assistantMu.Unlock()
	return a.assistant
}

// assistantSession 第一次用到时才开只读连接，没用过助手的人不多占一个连接。
func (a *App) assistantSession() (*assistant.Session, error) {
	a.assistantMu.Lock()
	defer a.assistantMu.Unlock()
	if a.assistant != nil {
		return a.assistant, nil
	}
	if a.dbManager == nil {
		return nil, errors.New("数据库还没准备好")
	}
	sandbox, err := assistant.OpenSandbox(a.dbManager.Path())
	if err != nil {
		return nil, err
	}
	session := assistant.NewSession(sandbox, desktopUserID, assistantConversationsDir)
	session.OnStep = func(step assistant.Step) {
		wailsRuntime.EventsEmit(a.ctx, assistantStepEvent, step)
	}
	session.Executor = assistantExecutor{app: a}
	session.OnConfirm = func(proposal assistant.Proposal) {
		wailsRuntime.EventsEmit(a.ctx, assistantConfirmEvent, proposal)
	}
	a.assistant = session
	return session, nil
}
