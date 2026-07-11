package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ==================== V3: AI 场景识别与内容理解 ====================

// VideoChapter 视频章节（AI自动生成或手动标记）
type VideoChapter struct {
	ID          string    `json:"id" gorm:"primaryKey;type:text"`
	MediaID     string    `json:"media_id" gorm:"index;type:text;not null"`
	Title       string    `json:"title" gorm:"type:text;not null"`    // 章节标题
	StartTime   float64   `json:"start_time"`                         // 开始时间（秒）
	EndTime     float64   `json:"end_time"`                           // 结束时间（秒）
	Description string    `json:"description" gorm:"type:text"`       // 章节描述
	SceneType   string    `json:"scene_type" gorm:"type:text"`        // 场景类型: action/dialogue/landscape/credits 等
	Confidence  float64   `json:"confidence"`                         // AI识别置信度 0-1
	Source      string    `json:"source" gorm:"type:text;default:ai"` // 来源: ai / manual
	Thumbnail   string    `json:"thumbnail" gorm:"type:text"`         // 章节缩略图路径
	CreatedAt   time.Time `json:"created_at"`

	Media Media `json:"-" gorm:"foreignKey:MediaID"`
}

func (vc *VideoChapter) BeforeCreate(tx *gorm.DB) error {
	if vc.ID == "" {
		vc.ID = uuid.New().String()
	}
	return nil
}

// VideoHighlight 视频精彩片段
type VideoHighlight struct {
	ID        string    `json:"id" gorm:"primaryKey;type:text"`
	MediaID   string    `json:"media_id" gorm:"index;type:text;not null"`
	Title     string    `json:"title" gorm:"type:text;not null"`    // 片段标题
	StartTime float64   `json:"start_time"`                         // 开始时间（秒）
	EndTime   float64   `json:"end_time"`                           // 结束时间（秒）
	Score     float64   `json:"score"`                              // 精彩程度评分 0-10
	Tags      string    `json:"tags" gorm:"type:text"`              // 标签，逗号分隔
	Thumbnail string    `json:"thumbnail" gorm:"type:text"`         // 精彩片段缩略图
	GifPath   string    `json:"gif_path" gorm:"type:text"`          // GIF预览路径
	Source    string    `json:"source" gorm:"type:text;default:ai"` // 来源: ai / manual
	CreatedAt time.Time `json:"created_at"`

	Media Media `json:"-" gorm:"foreignKey:MediaID"`
}

func (vh *VideoHighlight) BeforeCreate(tx *gorm.DB) error {
	if vh.ID == "" {
		vh.ID = uuid.New().String()
	}
	return nil
}

// AIAnalysisTask AI分析任务
type AIAnalysisTask struct {
	ID          string     `json:"id" gorm:"primaryKey;type:text"`
	MediaID     string     `json:"media_id" gorm:"index;type:text;not null"`
	TaskType    string     `json:"task_type" gorm:"type:text;not null"`     // scene_detect / highlight / cover_select / chapter_gen
	Status      string     `json:"status" gorm:"type:text;default:pending"` // pending / running / completed / failed
	Progress    float64    `json:"progress"`                                // 0-100
	Result      string     `json:"result" gorm:"type:text"`                 // JSON格式的分析结果
	Error       string     `json:"error" gorm:"type:text"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func (at *AIAnalysisTask) BeforeCreate(tx *gorm.DB) error {
	if at.ID == "" {
		at.ID = uuid.New().String()
	}
	return nil
}

// ==================== V3: AI 驱动的封面优化 ====================

// CoverCandidate 封面候选帧
type CoverCandidate struct {
	ID          string    `json:"id" gorm:"primaryKey;type:text"`
	MediaID     string    `json:"media_id" gorm:"index;type:text;not null"`
	FrameTime   float64   `json:"frame_time"`                           // 帧时间点（秒）
	ImagePath   string    `json:"image_path" gorm:"type:text;not null"` // 候选图片路径
	Score       float64   `json:"score"`                                // AI评分 0-10
	Brightness  float64   `json:"brightness"`                           // 亮度评分
	Sharpness   float64   `json:"sharpness"`                            // 清晰度评分
	Composition float64   `json:"composition"`                          // 构图评分
	FaceCount   int       `json:"face_count"`                           // 检测到的人脸数量
	IsSelected  bool      `json:"is_selected" gorm:"default:false"`     // 是否被选为封面
	CreatedAt   time.Time `json:"created_at"`

	Media Media `json:"-" gorm:"foreignKey:MediaID"`
}

func (cc *CoverCandidate) BeforeCreate(tx *gorm.DB) error {
	if cc.ID == "" {
		cc.ID = uuid.New().String()
	}
	return nil
}

// ==================== V3: 家庭社交互动功能 ====================

// FamilyGroup 家庭组
type FamilyGroup struct {
	ID         string         `json:"id" gorm:"primaryKey;type:text"`
	Name       string         `json:"name" gorm:"type:text;not null"`           // 家庭组名称
	OwnerID    string         `json:"owner_id" gorm:"index;type:text;not null"` // 创建者/管理员
	InviteCode string         `json:"invite_code" gorm:"uniqueIndex;type:text"` // 邀请码
	Avatar     string         `json:"avatar" gorm:"type:text"`                  // 家庭组头像
	MaxMembers int            `json:"max_members" gorm:"default:10"`            // 最大成员数
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
	DeletedAt  gorm.DeletedAt `json:"-" gorm:"index"`

	Owner   User           `json:"-" gorm:"foreignKey:OwnerID"`
	Members []FamilyMember `json:"members,omitempty" gorm:"foreignKey:GroupID"`
}

func (fg *FamilyGroup) BeforeCreate(tx *gorm.DB) error {
	if fg.ID == "" {
		fg.ID = uuid.New().String()
	}
	return nil
}

// FamilyMember 家庭组成员
type FamilyMember struct {
	ID       string    `json:"id" gorm:"primaryKey;type:text"`
	GroupID  string    `json:"group_id" gorm:"index;type:text;not null"`
	UserID   string    `json:"user_id" gorm:"index;type:text;not null"`
	Nickname string    `json:"nickname" gorm:"type:text"`            // 在家庭组中的昵称
	Role     string    `json:"role" gorm:"type:text;default:member"` // owner / admin / member
	JoinedAt time.Time `json:"joined_at"`

	Group FamilyGroup `json:"-" gorm:"foreignKey:GroupID"`
	User  User        `json:"user,omitempty" gorm:"foreignKey:UserID"`
}

func (fm *FamilyMember) BeforeCreate(tx *gorm.DB) error {
	if fm.ID == "" {
		fm.ID = uuid.New().String()
	}
	return nil
}

// MediaShare 视频分享记录
type MediaShare struct {
	ID        string    `json:"id" gorm:"primaryKey;type:text"`
	UserID    string    `json:"user_id" gorm:"index;type:text;not null"`       // 分享者
	GroupID   string    `json:"group_id" gorm:"index;type:text;not null"`      // 目标家庭组
	MediaID   string    `json:"media_id" gorm:"index;type:text;default:null"`  // 分享的媒体（可选）
	SeriesID  string    `json:"series_id" gorm:"index;type:text;default:null"` // 分享的剧集（可选）
	Message   string    `json:"message" gorm:"type:text"`                      // 分享附言
	CreatedAt time.Time `json:"created_at"`

	User   User        `json:"user,omitempty" gorm:"foreignKey:UserID"`
	Group  FamilyGroup `json:"-" gorm:"foreignKey:GroupID"`
	Media  *Media      `json:"media,omitempty" gorm:"foreignKey:MediaID"`
	Series *Series     `json:"series,omitempty" gorm:"foreignKey:SeriesID"`
}

func (ms *MediaShare) BeforeCreate(tx *gorm.DB) error {
	if ms.ID == "" {
		ms.ID = uuid.New().String()
	}
	return nil
}

// MediaLike 点赞记录
type MediaLike struct {
	ID        string    `json:"id" gorm:"primaryKey;type:text"`
	UserID    string    `json:"user_id" gorm:"index;type:text;not null"`
	MediaID   string    `json:"media_id" gorm:"index;type:text;default:null"`
	SeriesID  string    `json:"series_id" gorm:"index;type:text;default:null"`
	CommentID string    `json:"comment_id" gorm:"index;type:text;default:null"` // 也可以点赞评论
	CreatedAt time.Time `json:"created_at"`

	User User `json:"-" gorm:"foreignKey:UserID"`
}

func (ml *MediaLike) BeforeCreate(tx *gorm.DB) error {
	if ml.ID == "" {
		ml.ID = uuid.New().String()
	}
	return nil
}

// MediaRecommendation 用户推荐（家庭成员间推荐）
type MediaRecommendation struct {
	ID         string    `json:"id" gorm:"primaryKey;type:text"`
	FromUserID string    `json:"from_user_id" gorm:"index;type:text;not null"` // 推荐者
	ToUserID   string    `json:"to_user_id" gorm:"index;type:text;not null"`   // 被推荐者
	MediaID    string    `json:"media_id" gorm:"index;type:text;default:null"`
	SeriesID   string    `json:"series_id" gorm:"index;type:text;default:null"`
	Message    string    `json:"message" gorm:"type:text"` // 推荐理由
	IsRead     bool      `json:"is_read" gorm:"default:false"`
	CreatedAt  time.Time `json:"created_at"`

	FromUser User    `json:"from_user,omitempty" gorm:"foreignKey:FromUserID"`
	ToUser   User    `json:"-" gorm:"foreignKey:ToUserID"`
	Media    *Media  `json:"media,omitempty" gorm:"foreignKey:MediaID"`
	Series   *Series `json:"series,omitempty" gorm:"foreignKey:SeriesID"`
}

func (mr *MediaRecommendation) BeforeCreate(tx *gorm.DB) error {
	if mr.ID == "" {
		mr.ID = uuid.New().String()
	}
	return nil
}

// ==================== V3: 实时直播扩展 ====================

// LiveSource 直播源
type LiveSource struct {
	ID          string         `json:"id" gorm:"primaryKey;type:text"`
	Name        string         `json:"name" gorm:"type:text;not null"`     // 直播源名称
	URL         string         `json:"url" gorm:"type:text;not null"`      // 直播源地址（m3u8/rtmp/rtsp等）
	Type        string         `json:"type" gorm:"type:text;default:iptv"` // iptv / custom / rtmp
	Category    string         `json:"category" gorm:"type:text"`          // 分类：央视/卫视/地方/体育/电影等
	Logo        string         `json:"logo" gorm:"type:text"`              // 频道Logo
	EPGUrl      string         `json:"epg_url" gorm:"type:text"`           // 电子节目单URL
	Quality     string         `json:"quality" gorm:"type:text"`           // 画质标识: SD/HD/FHD/4K
	IsActive    bool           `json:"is_active" gorm:"default:true"`      // 是否可用
	SortOrder   int            `json:"sort_order" gorm:"default:0"`
	LastCheckAt *time.Time     `json:"last_check_at"`                 // 上次检测时间
	CheckStatus string         `json:"check_status" gorm:"type:text"` // ok / timeout / error
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `json:"-" gorm:"index"`
}

func (ls *LiveSource) BeforeCreate(tx *gorm.DB) error {
	if ls.ID == "" {
		ls.ID = uuid.New().String()
	}
	return nil
}

// LivePlaylist 直播播放列表（M3U导入）
type LivePlaylist struct {
	ID          string     `json:"id" gorm:"primaryKey;type:text"`
	Name        string     `json:"name" gorm:"type:text;not null"`
	URL         string     `json:"url" gorm:"type:text"`             // M3U文件URL（远程）
	FilePath    string     `json:"file_path" gorm:"type:text"`       // M3U文件本地路径
	SourceCount int        `json:"source_count" gorm:"default:0"`    // 包含的频道数
	AutoUpdate  bool       `json:"auto_update" gorm:"default:false"` // 是否自动更新
	UpdateCron  string     `json:"update_cron" gorm:"type:text"`     // 更新cron表达式
	LastUpdate  *time.Time `json:"last_update"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func (lp *LivePlaylist) BeforeCreate(tx *gorm.DB) error {
	if lp.ID == "" {
		lp.ID = uuid.New().String()
	}
	return nil
}

// LiveRecording 直播录制记录
type LiveRecording struct {
	ID          string     `json:"id" gorm:"primaryKey;type:text"`
	SourceID    string     `json:"source_id" gorm:"index;type:text;not null"` // 关联直播源
	UserID      string     `json:"user_id" gorm:"index;type:text;not null"`   // 录制发起者
	Title       string     `json:"title" gorm:"type:text;not null"`
	FilePath    string     `json:"file_path" gorm:"type:text"` // 录制文件路径
	FileSize    int64      `json:"file_size"`
	Duration    float64    `json:"duration"`                                  // 录制时长（秒）
	Status      string     `json:"status" gorm:"type:text;default:recording"` // recording / completed / failed
	StartedAt   time.Time  `json:"started_at"`
	StoppedAt   *time.Time `json:"stopped_at"`
	ScheduledAt *time.Time `json:"scheduled_at"` // 预约录制时间
	CreatedAt   time.Time  `json:"created_at"`

	Source LiveSource `json:"source,omitempty" gorm:"foreignKey:SourceID"`
	User   User       `json:"-" gorm:"foreignKey:UserID"`
}

func (lr *LiveRecording) BeforeCreate(tx *gorm.DB) error {
	if lr.ID == "" {
		lr.ID = uuid.New().String()
	}
	return nil
}

// ==================== V3: 云端同步与多设备支持 ====================

// SyncDevice 同步设备
type SyncDevice struct {
	ID           string     `json:"id" gorm:"primaryKey;type:text"`
	UserID       string     `json:"user_id" gorm:"index;type:text;not null"`
	DeviceName   string     `json:"device_name" gorm:"type:text;not null"`           // 设备名称
	DeviceType   string     `json:"device_type" gorm:"type:text;not null"`           // phone / tablet / tv / desktop / browser
	DeviceID     string     `json:"device_id" gorm:"uniqueIndex;type:text;not null"` // 设备唯一标识
	Platform     string     `json:"platform" gorm:"type:text"`                       // ios / android / windows / macos / linux / web
	AppVersion   string     `json:"app_version" gorm:"type:text"`
	LastSyncAt   *time.Time `json:"last_sync_at"`
	LastActiveAt *time.Time `json:"last_active_at"`
	IsOnline     bool       `json:"is_online" gorm:"default:false"`
	PushToken    string     `json:"push_token" gorm:"type:text"` // 推送通知Token
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`

	User User `json:"-" gorm:"foreignKey:UserID"`
}

func (sd *SyncDevice) BeforeCreate(tx *gorm.DB) error {
	if sd.ID == "" {
		sd.ID = uuid.New().String()
	}
	return nil
}

// SyncRecord 同步记录
type SyncRecord struct {
	ID        string    `json:"id" gorm:"primaryKey;type:text"`
	UserID    string    `json:"user_id" gorm:"index;type:text;not null"`
	DeviceID  string    `json:"device_id" gorm:"index;type:text;not null"`
	DataType  string    `json:"data_type" gorm:"type:text;not null"` // progress / favorites / playlists / settings / history
	DataKey   string    `json:"data_key" gorm:"type:text"`           // 数据标识（如mediaID）
	DataValue string    `json:"data_value" gorm:"type:text"`         // JSON格式的数据
	Version   int64     `json:"version" gorm:"default:0"`            // 数据版本号（用于冲突检测）
	SyncedAt  time.Time `json:"synced_at"`
	CreatedAt time.Time `json:"created_at"`
}

func (sr *SyncRecord) BeforeCreate(tx *gorm.DB) error {
	if sr.ID == "" {
		sr.ID = uuid.New().String()
	}
	return nil
}

// UserSyncConfig 用户同步配置
type UserSyncConfig struct {
	ID              string    `json:"id" gorm:"primaryKey;type:text"`
	UserID          string    `json:"user_id" gorm:"uniqueIndex;type:text;not null"`
	SyncProgress    bool      `json:"sync_progress" gorm:"default:true"`  // 同步观看进度
	SyncFavorites   bool      `json:"sync_favorites" gorm:"default:true"` // 同步收藏
	SyncPlaylists   bool      `json:"sync_playlists" gorm:"default:true"` // 同步播放列表
	SyncHistory     bool      `json:"sync_history" gorm:"default:true"`   // 同步观看历史
	SyncSettings    bool      `json:"sync_settings" gorm:"default:true"`  // 同步用户设置
	AutoSync        bool      `json:"auto_sync" gorm:"default:true"`      // 自动同步
	SyncIntervalMin int       `json:"sync_interval_min" gorm:"default:5"` // 同步间隔（分钟）
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`

	User User `json:"-" gorm:"foreignKey:UserID"`
}

func (usc *UserSyncConfig) BeforeCreate(tx *gorm.DB) error {
	if usc.ID == "" {
		usc.ID = uuid.New().String()
	}
	return nil
}
