import React, { useEffect, useState } from 'react';
import {
    Clock3,
    Download,
    Eye,
    EyeOff,
    Globe,
    Image,
    Images,
    Info,
    Link2,
    LockKeyhole,
    Monitor,
    Play,
    Power,
    Save,
    Search,
    Server,
    Settings,
    UserRound,
} from 'lucide-react';
import { GetDesktopSettings, SelectProgram, UpdateDesktopSettings } from "../../wailsjs/go/main/App";

type TabKey = 'general' | 'scan' | 'remote' | 'about';
type FeedbackTone = 'success' | 'error' | null;

const TEXT = {
    unknownError: '未知错误',
    loadFailed: '加载设置失败',
    saveSuccess: '设置已保存',
    saveFailed: '保存失败',
    selectProgramFailed: '选择程序失败',
    loading: '加载中...',
    settings: '设置',
    general: '常规设置',
    generalIntro: '管理应用程序的常用行为和外观设置。',
    scan: '扫描',
    scanIntro: '配置扫描行为与外部服务集成，让媒体扫描更高效准确。',
    remoteAccess: '远程 Jellyfin',
    remoteAccessIntro: 'Infuse 可以通过 Jellyfin Sidecar 连接这个媒体库。',
    about: '关于',
    aboutIntro: '了解当前设置中心的设计范围与后续扩展方向。',
    startup: '开机启动',
    startupDesc: '启动系统后自动运行 Navi。',
    tray: '最小化到托盘',
    trayDesc: '关闭主窗口时保留在系统托盘中。',
    externalPlayer: '使用外部播放器',
    externalPlayerDesc: '播放视频时优先使用你指定的本地播放器。',
    externalPlayerName: '外部播放器',
    externalPlayerHint: '未选择时，将使用系统默认播放器打开。',
    chooseProgram: '选择程序',
    clear: '清空',
    save: '保存',
    saveMeta: '更改会直接写入本地桌面端配置。',
    unsaved: '有未保存更改',
    synced: '配置已同步',
    useEverything: '调用 Everything',
    useEverythingDesc: '扫描电影库时优先走 Everything HTTP 盘点文件和计数，新增刷新、删改刷新都会更快。',
    everythingAddr: 'Everything 网址',
    everythingAddrDesc: '填写 Everything HTTP 服务地址，例如 http://127.0.0.1:8077。',
    everythingAddrPlaceholder: 'http://127.0.0.1:8077',
    everythingTipTitle: '扫描主流程说明',
    everythingTip: 'Everything 现在会参与扫描主流程；正式入库前仍会对命中文件做本地校验，避免索引延迟误判。',
    videoThumbnail: '自动截图补图',
    videoThumbnailDesc: '只对无 NFO 且时长足够的电影视频生成 poster、fanart 和预览图。',
    gfriendsAvatars: 'gfriends 演员头像',
    gfriendsAvatarsDesc: '按需联网匹配 gfriends Filetree，并把命中的演员头像缓存到本地。',
    thumbnailMinDuration: '最短时长（分钟）',
    thumbnailMinDurationDesc: '只有严格大于这个时长的视频才会触发自动截图。',
    thumbnailPreviewCount: '预览图数量',
    thumbnailPreviewCountDesc: '生成到 extrafanart 目录的预览图张数。',
    remoteBindHost: '监听地址',
    remoteBindHostDesc: '填写 0.0.0.0 可以被局域网设备访问，127.0.0.1 则只限本机。',
    remoteUsername: '远程用户名',
    remoteUsernameDesc: 'Jellyfin sidecar 使用这组登录凭据。',
    remotePassword: '远程密码',
    remotePasswordDesc: '建议使用一个单独的 Infuse 连接密码。',
    jellyfinEnabled: '启用 Jellyfin Sidecar',
    jellyfinEnabledDesc: '提供 Infuse 更熟悉的媒体库、海报、播放和进度接口。',
    jellyfinPort: 'Jellyfin 端口',
    jellyfinPortDesc: '默认 18096。Infuse 里可按 Jellyfin 服务器添加。',
    jellyfinServerName: '服务名称',
    jellyfinServerNameDesc: '客户端里显示的服务器名称。',
    jellyfinHint: 'Jellyfin 地址',
    remoteLanHint: '如果监听地址填写 0.0.0.0，下面的 URL 里请把 0.0.0.0 替换成这台机器的局域网 IP。',
    showPassword: '显示密码',
    hidePassword: '隐藏密码',
    aboutCardTitle: '设置中心',
    aboutCardDesc: '本次重构聚焦设置界面本身，关于页暂时保留轻量说明，不扩展额外业务信息。',
    aboutCardNote: '当前页面只承载设置导航结构和视觉统一性，不改变现有业务功能范围。',
} as const;

const cloneSettings = <T,>(value: T): T => JSON.parse(JSON.stringify(value));

const formatError = (error: unknown) => {
    if (error instanceof Error && error.message) {
        return error.message;
    }
    if (typeof error === 'string') {
        return error;
    }
    return TEXT.unknownError;
};

const getProgramName = (programPath: string) => {
    if (!programPath) {
        return '';
    }
    const normalized = programPath.replace(/\\/g, '/');
    return normalized.split('/').pop() || programPath;
};

const getThumbnailDurationMinutes = (seconds: unknown) => {
    const parsed = Number(seconds);
    if (!Number.isFinite(parsed) || parsed <= 0) {
        return 20;
    }
    return Math.max(1, Math.round(parsed / 60));
};

const getThumbnailPreviewCount = (count: unknown) => {
    const parsed = Number(count);
    if (!Number.isFinite(parsed) || parsed <= 0) {
        return 6;
    }
    return Math.max(1, Math.min(12, Math.round(parsed)));
};

type NavItem = {
    key: TabKey;
    label: string;
    description: string;
    icon: React.ElementType;
};

type PageMeta = {
    title: string;
    description: string;
};

type SettingRowProps = {
    icon: React.ElementType;
    title: string;
    description: string;
    control: React.ReactNode;
    stacked?: boolean;
    disabled?: boolean;
};

type RemoteSectionProps = {
    icon: React.ElementType;
    title: string;
    description: string;
    children?: React.ReactNode;
    disabled?: boolean;
    aside?: React.ReactNode;
    className?: string;
};

const NAV_ITEMS: NavItem[] = [
    { key: 'general', label: TEXT.general, description: TEXT.generalIntro, icon: Settings },
    { key: 'scan', label: TEXT.scan, description: TEXT.scanIntro, icon: Search },
    { key: 'remote', label: TEXT.remoteAccess, description: TEXT.remoteAccessIntro, icon: Server },
    { key: 'about', label: TEXT.about, description: TEXT.aboutIntro, icon: Info },
];

const PAGE_META: Record<TabKey, PageMeta> = {
    general: { title: TEXT.general, description: TEXT.generalIntro },
    scan: { title: '扫描设置', description: TEXT.scanIntro },
    remote: { title: TEXT.remoteAccess, description: TEXT.remoteAccessIntro },
    about: { title: TEXT.about, description: TEXT.aboutIntro },
};

const SettingSwitch: React.FC<{
    checked: boolean;
    disabled?: boolean;
    onChange: (checked: boolean) => void;
}> = ({ checked, disabled = false, onChange }) => (
    <button
        type="button"
        className={`settings-switch ${checked ? 'is-checked' : ''}`}
        role="switch"
        aria-checked={checked}
        disabled={disabled}
        onClick={() => onChange(!checked)}
    >
        <span className="settings-switch-thumb" />
    </button>
);

const SettingRow: React.FC<SettingRowProps> = ({
    icon: Icon,
    title,
    description,
    control,
    stacked = false,
    disabled = false,
}) => (
    <div className={`settings-row-card ${stacked ? 'is-stacked' : ''} ${disabled ? 'is-disabled' : ''}`}>
        <div className="settings-row-meta">
            <div className="settings-row-icon" aria-hidden="true">
                <Icon size={20} strokeWidth={1.9} />
            </div>
            <div className="settings-row-copy">
                <div className="settings-row-title">{title}</div>
                <div className="settings-row-description">{description}</div>
            </div>
        </div>
        <div className={`settings-row-control ${stacked ? 'is-stacked' : ''}`}>{control}</div>
    </div>
);

const RemoteSection: React.FC<RemoteSectionProps> = ({
    icon: Icon,
    title,
    description,
    children,
    disabled = false,
    aside,
    className = '',
}) => (
    <section className={`settings-remote-section ${disabled ? 'is-disabled' : ''} ${className}`.trim()}>
        <div className="settings-remote-section-header">
            <div className="settings-row-icon" aria-hidden="true">
                <Icon size={20} strokeWidth={1.9} />
            </div>
            <div className="settings-remote-section-copy">
                <div className="settings-row-title">{title}</div>
                <div className="settings-row-description">{description}</div>
            </div>
            {aside && <div className="settings-remote-section-aside">{aside}</div>}
        </div>
        {children && <div className="settings-remote-section-body">{children}</div>}
    </section>
);

const SettingsPage: React.FC = () => {
    const [settings, setSettings] = useState<any>(null);
    const [savedSettings, setSavedSettings] = useState<any>(null);
    const [activeTab, setActiveTab] = useState<TabKey>('general');
    const [msg, setMsg] = useState('');
    const [feedbackTone, setFeedbackTone] = useState<FeedbackTone>(null);
    const [showPassword, setShowPassword] = useState(false);

    useEffect(() => {
        GetDesktopSettings()
            .then((res: any) => {
                if (!res) {
                    return;
                }
                const nextSettings = cloneSettings(res);
                setSettings(nextSettings);
                setSavedSettings(cloneSettings(nextSettings));
            })
            .catch((error) => {
                setMsg(`${TEXT.loadFailed}：${formatError(error)}`);
                setFeedbackTone('error');
            });
    }, []);

    const hasChanges = !!settings && !!savedSettings && JSON.stringify(settings) !== JSON.stringify(savedSettings);

    const clearFeedback = () => {
        setMsg('');
        setFeedbackTone(null);
    };

    const updateSettings = (patch: Record<string, unknown>) => {
        setSettings((prev: any) => (prev ? { ...prev, ...patch } : prev));
        clearFeedback();
    };

    const handleSave = async () => {
        if (!settings) {
            return;
        }
        try {
            await UpdateDesktopSettings(settings);
            const nextSaved = cloneSettings(settings);
            setSavedSettings(nextSaved);
            setSettings(cloneSettings(nextSaved));
            setMsg(TEXT.saveSuccess);
            setFeedbackTone('success');
            window.setTimeout(() => {
                setMsg('');
                setFeedbackTone(null);
            }, 2500);
        } catch (error) {
            setMsg(`${TEXT.saveFailed}：${formatError(error)}`);
            setFeedbackTone('error');
        }
    };

    const handleSelectProgram = async () => {
        try {
            const programPath = await SelectProgram();
            if (!programPath) {
                return;
            }
            updateSettings({ player_path: programPath });
        } catch (error) {
            setMsg(`${TEXT.selectProgramFailed}：${formatError(error)}`);
            setFeedbackTone('error');
        }
    };

    const handleClearProgram = () => {
        updateSettings({ player_path: '' });
    };

    const renderStatus = () => {
        if (msg) {
            return (
                <div className={`settings-status-pill ${feedbackTone ? `is-${feedbackTone}` : ''}`}>
                    {msg}
                </div>
            );
        }

        return (
            <div className={`settings-status-pill ${hasChanges ? 'is-pending' : ''}`}>
                {hasChanges ? TEXT.unsaved : TEXT.synced}
            </div>
        );
    };

    const renderSaveFooter = () => (
        <div className="settings-page-footer">
            <div className="settings-page-footer-main">
                <button
                    className="settings-primary-button"
                    type="button"
                    onClick={handleSave}
                    disabled={!hasChanges}
                >
                    <Save size={18} strokeWidth={1.9} />
                    <span>{TEXT.save}</span>
                </button>
            </div>
            {msg && feedbackTone === 'error' && (
                <div className={`settings-feedback-inline ${feedbackTone ? `is-${feedbackTone}` : ''}`}>
                    {msg}
                </div>
            )}
        </div>
    );

    if (!settings) {
        return <div className="settings-loading">{TEXT.loading}</div>;
    }

    const currentMeta = PAGE_META[activeTab];
    const bindHost = settings.remote_bind_host || '0.0.0.0';
    const jellyfinPort = Number(settings.jellyfin_port) > 0 ? Number(settings.jellyfin_port) : 18096;
    const jellyfinURL = `http://${bindHost}:${jellyfinPort}`;

    const renderGeneral = () => (
        <>
            <section className="settings-panel settings-panel-list">
                <SettingRow
                    icon={Power}
                    title={TEXT.startup}
                    description={TEXT.startupDesc}
                    control={
                        <SettingSwitch
                            checked={!!settings.start_with_os}
                            onChange={(checked) => updateSettings({ start_with_os: checked })}
                        />
                    }
                />

                <SettingRow
                    icon={Download}
                    title={TEXT.tray}
                    description={TEXT.trayDesc}
                    control={
                        <SettingSwitch
                            checked={!!settings.min_to_tray}
                            onChange={(checked) => updateSettings({ min_to_tray: checked })}
                        />
                    }
                />

                <SettingRow
                    icon={Play}
                    title={TEXT.externalPlayer}
                    description={TEXT.externalPlayerDesc}
                    control={
                        <SettingSwitch
                            checked={!!settings.use_external_player}
                            onChange={(checked) => updateSettings({ use_external_player: checked })}
                        />
                    }
                />

                <SettingRow
                    icon={Monitor}
                    title={TEXT.externalPlayerName}
                    description={TEXT.externalPlayerHint}
                    stacked
                    disabled={!settings.use_external_player}
                    control={
                        <div className="settings-input-action-row">
                            <input
                                className="settings-input"
                                type="text"
                                value={getProgramName(settings.player_path || '')}
                                placeholder={TEXT.externalPlayerHint}
                                title={settings.player_path || ''}
                                readOnly
                                disabled={!settings.use_external_player}
                            />

                            <div className="settings-inline-actions">
                                <button
                                    className="settings-secondary-button settings-secondary-button-primary"
                                    type="button"
                                    onClick={handleSelectProgram}
                                    disabled={!settings.use_external_player}
                                >
                                    {TEXT.chooseProgram}
                                </button>
                                <button
                                    className="settings-secondary-button"
                                    type="button"
                                    onClick={handleClearProgram}
                                    disabled={!settings.use_external_player || !settings.player_path}
                                >
                                    {TEXT.clear}
                                </button>
                            </div>
                        </div>
                    }
                />
            </section>

            {renderSaveFooter()}
        </>
    );

    const renderScan = () => (
        <>
            <section className="settings-panel settings-panel-list">
                <SettingRow
                    icon={Link2}
                    title={TEXT.useEverything}
                    description={TEXT.useEverythingDesc}
                    control={
                        <SettingSwitch
                            checked={!!settings.use_everything}
                            onChange={(checked) => updateSettings({ use_everything: checked })}
                        />
                    }
                />

                <SettingRow
                    icon={Globe}
                    title={TEXT.everythingAddr}
                    description={TEXT.everythingAddrDesc}
                    stacked
                    disabled={!settings.use_everything}
                    control={
                        <input
                            className="settings-input"
                            type="text"
                            value={settings.everything_addr || ''}
                            placeholder={TEXT.everythingAddrPlaceholder}
                            onChange={(e) => updateSettings({ everything_addr: e.target.value })}
                            disabled={!settings.use_everything}
                        />
                    }
                />

                <div className="settings-hint-card">
                    <div className="settings-row-icon" aria-hidden="true">
                        <Info size={20} strokeWidth={1.9} />
                    </div>
                    <div className="settings-hint-copy">
                        <div className="settings-row-description settings-hint-description">{TEXT.everythingTip}</div>
                    </div>
                </div>

                <SettingRow
                    icon={Image}
                    title={TEXT.videoThumbnail}
                    description={TEXT.videoThumbnailDesc}
                    control={
                        <SettingSwitch
                            checked={!!settings.enable_video_thumbnail}
                            onChange={(checked) => updateSettings({ enable_video_thumbnail: checked })}
                        />
                    }
                />

                <SettingRow
                    icon={UserRound}
                    title={TEXT.gfriendsAvatars}
                    description={TEXT.gfriendsAvatarsDesc}
                    control={
                        <SettingSwitch
                            checked={!!settings.enable_gfriends_avatars}
                            onChange={(checked) => updateSettings({ enable_gfriends_avatars: checked })}
                        />
                    }
                />

                <SettingRow
                    icon={Clock3}
                    title={TEXT.thumbnailMinDuration}
                    description={TEXT.thumbnailMinDurationDesc}
                    control={
                        <input
                            className="settings-input settings-input-compact settings-number-input"
                            type="number"
                            min={1}
                            step={1}
                            value={getThumbnailDurationMinutes(settings.thumbnail_min_duration_seconds)}
                            onChange={(e) => updateSettings({
                                thumbnail_min_duration_seconds: Math.max(1, Number(e.target.value) || 0) * 60,
                            })}
                            disabled={!settings.enable_video_thumbnail}
                        />
                    }
                    disabled={!settings.enable_video_thumbnail}
                />

                <SettingRow
                    icon={Images}
                    title={TEXT.thumbnailPreviewCount}
                    description={TEXT.thumbnailPreviewCountDesc}
                    control={
                        <input
                            className="settings-input settings-input-compact settings-number-input"
                            type="number"
                            min={1}
                            max={12}
                            step={1}
                            value={getThumbnailPreviewCount(settings.thumbnail_preview_count)}
                            onChange={(e) => updateSettings({
                                thumbnail_preview_count: Math.max(1, Math.min(12, Number(e.target.value) || 0)),
                            })}
                            disabled={!settings.enable_video_thumbnail}
                        />
                    }
                    disabled={!settings.enable_video_thumbnail}
                />
            </section>

            {renderSaveFooter()}
        </>
    );

    const renderRemote = () => (
        <>
            <section className="settings-panel settings-remote-panel">
                <div className="settings-remote-columns">
                    <div className="settings-remote-column">
                        <RemoteSection
                            icon={Globe}
                            title={TEXT.remoteBindHost}
                            description={TEXT.remoteBindHostDesc}
                        >
                            <input
                                className="settings-input"
                                type="text"
                                value={settings.remote_bind_host || ''}
                                placeholder="0.0.0.0"
                                onChange={(e) => updateSettings({ remote_bind_host: e.target.value })}
                            />
                        </RemoteSection>

                        <RemoteSection
                            icon={UserRound}
                            title={TEXT.remoteUsername}
                            description={TEXT.remoteUsernameDesc}
                        >
                            <input
                                className="settings-input"
                                type="text"
                                value={settings.remote_username || ''}
                                onChange={(e) => updateSettings({ remote_username: e.target.value })}
                            />
                        </RemoteSection>

                        <RemoteSection
                            icon={LockKeyhole}
                            title={TEXT.remotePassword}
                            description={TEXT.remotePasswordDesc}
                        >
                            <div className="settings-password-shell">
                                <input
                                    className="settings-input settings-password-input"
                                    type={showPassword ? 'text' : 'password'}
                                    value={settings.remote_password || ''}
                                    onChange={(e) => updateSettings({ remote_password: e.target.value })}
                                />
                                <button
                                    type="button"
                                    className="settings-password-toggle"
                                    onClick={() => setShowPassword((prev) => !prev)}
                                    aria-label={showPassword ? TEXT.hidePassword : TEXT.showPassword}
                                    title={showPassword ? TEXT.hidePassword : TEXT.showPassword}
                                >
                                    {showPassword ? <EyeOff size={18} strokeWidth={1.9} /> : <Eye size={18} strokeWidth={1.9} />}
                                </button>
                            </div>
                        </RemoteSection>

                        <RemoteSection
                            icon={Play}
                            title={TEXT.jellyfinEnabled}
                            description={TEXT.jellyfinEnabledDesc}
                            aside={
                                <SettingSwitch
                                    checked={!!settings.jellyfin_enabled}
                                    onChange={(checked) => updateSettings({ jellyfin_enabled: checked })}
                                />
                            }
                            className="settings-remote-section-toggle"
                        />
                    </div>

                    <div className="settings-remote-column">
                        <RemoteSection
                            icon={Server}
                            title={TEXT.jellyfinPort}
                            description={TEXT.jellyfinPortDesc}
                            disabled={!settings.jellyfin_enabled}
                        >
                            <input
                                className="settings-input"
                                type="number"
                                min={1}
                                max={65535}
                                step={1}
                                value={jellyfinPort}
                                onChange={(e) => updateSettings({ jellyfin_port: Math.max(1, Number(e.target.value) || 0) })}
                                disabled={!settings.jellyfin_enabled}
                            />
                        </RemoteSection>

                        <RemoteSection
                            icon={Settings}
                            title={TEXT.jellyfinServerName}
                            description={TEXT.jellyfinServerNameDesc}
                            disabled={!settings.jellyfin_enabled}
                        >
                            <input
                                className="settings-input"
                                type="text"
                                value={settings.jellyfin_server_name || ''}
                                onChange={(e) => updateSettings({ jellyfin_server_name: e.target.value })}
                                disabled={!settings.jellyfin_enabled}
                            />
                        </RemoteSection>

                        <div className="settings-remote-divider" />

                        <RemoteSection
                            icon={Info}
                            title={TEXT.jellyfinHint}
                            description={TEXT.remoteLanHint}
                            className="settings-remote-section-note"
                        >
                            <div className="settings-address-card">
                                <div className="settings-address-label">{TEXT.jellyfinHint}</div>
                                <div className="settings-address-value">{jellyfinURL}</div>
                            </div>
                        </RemoteSection>
                    </div>
                </div>
            </section>

            {renderSaveFooter()}
        </>
    );

    const renderAbout = () => (
        <section className="settings-panel settings-about-panel">
            <div className="settings-about-mark" aria-hidden="true">
                <Info size={34} strokeWidth={1.8} />
            </div>
            <div className="settings-about-copy">
                <div className="settings-about-title">{TEXT.aboutCardTitle}</div>
                <div className="settings-about-description">{TEXT.aboutCardDesc}</div>
                <div className="settings-about-note">{TEXT.aboutCardNote}</div>
            </div>
        </section>
    );

    const renderContent = () => {
        switch (activeTab) {
            case 'general':
                return renderGeneral();
            case 'scan':
                return renderScan();
            case 'remote':
                return renderRemote();
            case 'about':
            default:
                return renderAbout();
        }
    };

    return (
        <div className="settings-container">
            <div className="settings-layout">
                <aside className="settings-sidebar">
                    <div className="settings-nav-list">
                        {NAV_ITEMS.map((item) => {
                            const Icon = item.icon;
                            const isActive = activeTab === item.key;
                            return (
                                <button
                                    key={item.key}
                                    type="button"
                                    className={`settings-nav-item ${isActive ? 'active' : ''}`}
                                    onClick={() => setActiveTab(item.key)}
                                >
                                    <span className="settings-nav-indicator" aria-hidden="true" />
                                    <span className="settings-nav-icon" aria-hidden="true">
                                        <Icon size={20} strokeWidth={1.85} />
                                    </span>
                                    <span className="settings-nav-label">{item.label}</span>
                                </button>
                            );
                        })}
                    </div>

                    <div className="settings-sidebar-spacer" />
                </aside>

                <main className="settings-main">
                    <div className="settings-main-scroll">
                        <div className="settings-page-hero">
                            <div className="settings-page-hero-copy">
                                <h1>{currentMeta.title}</h1>
                                <p>{currentMeta.description}</p>
                            </div>
                            <div className="settings-page-hero-side">
                                {renderStatus()}
                            </div>
                        </div>

                        {renderContent()}
                    </div>
                </main>
            </div>
        </div>
    );
};

export default SettingsPage;
