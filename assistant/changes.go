package assistant

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	toolProposeChange = "propose_change"
	toolOpenFolder    = "open_folder"
	toolPlayMedia     = "play_media"

	maxOpenFolders = 10
)

// confirmTimeout 是确认卡片最多等多久，人走开了也不至于让助手一直卡着。变量是为了测试能改短。
var confirmTimeout = 30 * time.Minute

// Change 是模型提出的一次修改。先给用户看确认卡片，用户点了执行才交给 Executor 落地。
type Change struct {
	Action       string            `json:"action"`
	MediaIDs     []string          `json:"media_ids,omitempty"`
	LibraryID    string            `json:"library_id,omitempty"`
	ScanMode     string            `json:"scan_mode,omitempty"`
	Rating       int               `json:"rating,omitempty"`
	Tag          string            `json:"tag,omitempty"`
	Fields       map[string]string `json:"fields,omitempty"`
	AddGenres    []string          `json:"add_genres,omitempty"`
	RemoveGenres []string          `json:"remove_genres,omitempty"`
	NewName      string            `json:"new_name,omitempty"` // rename_tag 用
	IntoTag      string            `json:"into_tag,omitempty"` // merge_tags 用：tag 并进这个标签
}

// ChangePreview 是 Executor 校验完参数后算出来的"会改什么"。
type ChangePreview struct {
	Title    string   // 一句话：要做什么
	Lines    []string // 每部片具体怎么改，或者额外的提醒
	MediaIDs []string // 卡片上展示的影片
	Danger   bool     // 删除这类改错了难挽回的，按钮标红
	// AlwaysConfirm 表示完全权限下也要用户点确认：改文件、移除、删文件这类。
	AlwaysConfirm bool
}

// Executor 由 App 实现，助手包自己不碰写库。
type Executor interface {
	Preview(change Change) (ChangePreview, error)
	Apply(change Change) (string, error)
	OpenFolder(mediaID string) (string, error)
	// Play 用播放器打开影片，返回番号或片名，界面上显示"正在播放 X"。
	Play(mediaID string) (string, error)
}

const (
	ProposalPending   = "pending"
	ProposalApplied   = "applied"
	ProposalCancelled = "cancelled"
	ProposalFailed    = "failed"
)

// Proposal 是界面上的一张确认卡片，执行完后连同结果一起留在会话里。
type Proposal struct {
	ID     string      `json:"id"`
	Title  string      `json:"title"`
	Lines  []string    `json:"lines"`
	Media  []MediaCard `json:"media"`
	Danger bool        `json:"danger"`
	Auto   bool        `json:"auto"` // 完全权限下没问用户就执行了
	Status string      `json:"status"`
	Result string      `json:"result"`
}

var errNoExecutor = errors.New("这个版本不能修改数据")

func newProposalID() string {
	buf := make([]byte, 6)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// proposeChange 出确认卡片，等用户点执行或取消（或者停止这句话），再把结果告诉模型。
func (s *Session) proposeChange(ctx context.Context, raw json.RawMessage, turn *turnState) (Step, string, error) {
	step := Step{Tool: toolProposeChange}
	if s.Executor == nil {
		return step, "", errNoExecutor
	}
	var change Change
	if err := json.Unmarshal(raw, &change); err != nil {
		return step, "", fmt.Errorf("参数不是合法 JSON：%w", err)
	}
	if turn.permission == PermissionReadOnly {
		return step, "", errors.New("现在是「仅可查看」权限，不能改。告诉用户在输入框下面把权限切到「可以修改」再说")
	}
	if change.Action == "delete_files" && turn.permission != PermissionFull {
		return step, "", errors.New("删视频文件要「完全权限」。告诉用户可以在输入框下面切到完全权限，或者改用 open_folder 打开文件夹让他自己删")
	}
	preview, err := s.Executor.Preview(change)
	if err != nil {
		return step, "", err
	}
	step.Input = preview.Title
	cards, err := s.sandbox.loadMediaCards(ctx, preview.MediaIDs, s.userID)
	if err != nil {
		return step, "", err
	}
	proposal := &Proposal{
		ID:     newProposalID(),
		Title:  preview.Title,
		Lines:  preview.Lines,
		Media:  cards,
		Danger: preview.Danger,
		Auto:   turn.permission == PermissionFull && !preview.AlwaysConfirm,
		Status: ProposalPending,
	}
	if proposal.Lines == nil {
		proposal.Lines = []string{}
	}

	approved, timedOut := proposal.Auto, false
	if !approved {
		approved, timedOut = s.waitForDecision(ctx, proposal)
	}

	var content string
	switch {
	case timedOut:
		proposal.Status = ProposalCancelled
		proposal.Result = "太久没人确认，没有执行"
		content = "用户很久没有确认，这次修改作废了，什么都没改。告诉用户需要的话再说一遍。"
	case !approved:
		proposal.Status = ProposalCancelled
		proposal.Result = "没有执行"
		content = "用户没有同意，什么都没改。问问用户想怎么调整，不要再原样提一遍。"
	default:
		result, err := s.Executor.Apply(change)
		if err != nil {
			proposal.Status = ProposalFailed
			proposal.Result = err.Error()
			content = "执行出错：" + err.Error()
		} else {
			proposal.Status = ProposalApplied
			proposal.Result = result
			content = "执行结果：" + result
			if !proposal.Auto {
				content = "用户已确认，" + content
			}
		}
	}
	step.Summary = proposal.Result
	turn.changes = append(turn.changes, *proposal)
	return step, content, nil
}

// waitForDecision 出确认卡片，等用户点执行或取消；这句话被停止也算取消，等太久算超时。
func (s *Session) waitForDecision(ctx context.Context, proposal *Proposal) (approved, timedOut bool) {
	decision := make(chan bool, 1)
	s.mu.Lock()
	s.pending, s.decision = proposal, decision
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.pending, s.decision = nil, nil
		s.mu.Unlock()
	}()
	if s.OnConfirm != nil {
		s.OnConfirm(*proposal)
	}

	timer := time.NewTimer(confirmTimeout)
	defer timer.Stop()
	select {
	case approved := <-decision:
		return approved, false
	case <-ctx.Done():
		return false, false
	case <-timer.C:
		return false, true
	}
}

// Decide 是确认卡片上的按钮：approve 为 true 执行，false 取消。
func (s *Session) Decide(id string, approve bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil || s.pending.ID != id {
		return errors.New("这张确认卡片已经失效了")
	}
	select {
	case s.decision <- approve:
	default: // 已经点过了
	}
	return nil
}

// Pending 返回正在等确认的卡片，没有就是 nil。切走页面再回来时靠它把卡片找回来。
func (s *Session) Pending() *Proposal {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		return nil
	}
	p := *s.pending
	return &p
}

func (s *Session) openFolder(raw json.RawMessage) (Step, string, error) {
	step := Step{Tool: toolOpenFolder}
	if s.Executor == nil {
		return step, "", errNoExecutor
	}
	var input struct {
		MediaIDs []string `json:"media_ids"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return step, "", fmt.Errorf("参数不是合法 JSON：%w", err)
	}
	if len(input.MediaIDs) == 0 {
		return step, "", errors.New("没给 media_ids")
	}
	if len(input.MediaIDs) > maxOpenFolders {
		return step, "", fmt.Errorf("一次最多打开 %d 个文件夹，别一下子弹出一堆窗口", maxOpenFolders)
	}
	opened := map[string]bool{}
	var lines []string
	for _, id := range input.MediaIDs {
		dir, err := s.Executor.OpenFolder(strings.TrimSpace(id))
		switch {
		case err != nil:
			lines = append(lines, id+"：打不开，"+err.Error())
		case !opened[dir]:
			opened[dir] = true
			lines = append(lines, "已打开 "+dir)
		}
	}
	step.Input = fmt.Sprintf("%d 部", len(input.MediaIDs))
	step.Summary = fmt.Sprintf("打开 %d 个文件夹", len(opened))
	return step, strings.Join(lines, "\n"), nil
}

// playMedia 直接开播，不用确认：播放不改库里的东西。播的那部顺带展示成卡片，用户知道放的是哪部。
func (s *Session) playMedia(ctx context.Context, raw json.RawMessage, turn *turnState) (Step, string, error) {
	step := Step{Tool: toolPlayMedia}
	if s.Executor == nil {
		return step, "", errNoExecutor
	}
	var input struct {
		MediaID string `json:"media_id"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return step, "", fmt.Errorf("参数不是合法 JSON：%w", err)
	}
	id := strings.TrimSpace(input.MediaID)
	if id == "" {
		return step, "", errors.New("没给 media_id")
	}
	label, err := s.Executor.Play(id)
	if err != nil {
		return step, "", fmt.Errorf("没放起来：%w", err)
	}
	step.Input = label
	step.Summary = "已开始播放 " + label
	if !turn.shown[id] {
		if cards, err := s.sandbox.loadMediaCards(ctx, []string{id}, s.userID); err == nil && len(cards) > 0 {
			turn.shown[id] = true
			turn.media = append(turn.media, cards...)
		}
	}
	return step, "已经在播放器里开始播放 " + label, nil
}
