import React, { useEffect, useState } from 'react';
import { Eye, EyeOff, Info, Search, Server, Settings } from 'lucide-react';
import { GetDesktopSettings, SelectProgram, UpdateDesktopSettings } from '../../wailsjs/go/main/App';

type TabKey = 'general' | 'scan' | 'remote' | 'about';
type FeedbackTone = 'success' | 'error' | null;

const TEXT = {
    unknownError: '未知错误',
    loadFailed: '加载设置失败',
    saveSuccess: '设置已保存',
    saveFailed: '保存失败',
    selectProgramFailed: '选择程序失败',
    loading: '加载中...',
    general: '常规',
    scan: '扫描',
    remoteAccess: '远程 Jellyfin',
    about: '关于',
    startup: '开机启动',
    startupDesc: '启动系统后自动运行 Navi。',
    tray: '最小化到托盘',
    trayDesc: '关闭主窗口时保留在系统托盘中。',
    externalPlayer: '使用外部播放器',
    externalPlayerDesc: '播放视频时优先使用你指定的本地播放器。',
    externalPlayerName: '播放器程序',
    externalPlayerHint: '未选择时，将使用系统默认播放器打开。',
    chooseProgram: '选择程序',
    clear: '清空',
    discard: '放弃更改',
    save: '保存',
    unsaved: '有未保存更改',
    synced: '配置已同步',
    useEverything: '调用 Everything',
    useEverythingDesc: '扫描媒体库时优先使用 Everything HTTP 服务。',
    everythingAddr: 'Everything 网址',
    everythingAddrDesc: '填写 Everything HTTP 服务地址。',
    everythingAddrPlaceholder: 'http://127.0.0.1:8077',
    everythingTip: '正式入库前仍会对命中文件做本地校验，避免索引延迟误判。',
    videoThumbnail: '自动截图补图',
    videoThumbnailDesc: '为缺少 NFO 且时长足够的视频生成海报和预览图。',
    gfriendsAvatars: 'gfriends 演员头像',
    gfriendsAvatarsDesc: '按需匹配并缓存演员头像。',
    thumbnailMinDuration: '最短时长（分钟）',
    thumbnailMinDurationDesc: '超过这个时长的视频才会触发自动截图。',
    thumbnailPreviewCount: '预览图数量',
    thumbnailPreviewCountDesc: '生成到 extrafanart 目录的预览图张数。',
    remoteBindHost: '监听地址',
    remoteBindHostDesc: '0.0.0.0 可供局域网访问，127.0.0.1 仅限本机。',
    remoteUsername: '远程用户名',
    remoteUsernameDesc: 'Jellyfin sidecar 使用的登录用户名。',
    remotePassword: '远程密码',
    remotePasswordDesc: '建议使用单独的 Infuse 连接密码。',
    jellyfinEnabled: '启用 Jellyfin Sidecar',
    jellyfinEnabledDesc: '提供媒体库、海报、播放和进度接口。',
    jellyfinPort: 'Jellyfin 端口',
    jellyfinPortDesc: '默认 18096。',
    jellyfinServerName: '服务名称',
    jellyfinServerNameDesc: '客户端里显示的服务器名称。',
    jellyfinHint: 'Jellyfin 地址',
    remoteLanHint: '使用 0.0.0.0 监听时，请将地址替换成这台机器的局域网 IP。',
    showPassword: '显示密码',
    hidePassword: '隐藏密码',
    aboutCardTitle: 'Navi 设置中心',
    aboutCardDesc: '集中管理桌面端行为、媒体扫描与远程访问。',
    aboutCardNote: '设置保存在本机，不会改变现有媒体库内容。',
} as const;

const cloneSettings = <T,>(value: T): T => JSON.parse(JSON.stringify(value));

const formatError = (error: unknown) => {
    if (error instanceof Error && error.message) {
        return error.message;
    }
    return typeof error === 'string' ? error : TEXT.unknownError;
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
    return Number.isFinite(parsed) && parsed > 0 ? Math.max(1, Math.round(parsed / 60)) : 20;
};

const getThumbnailPreviewCount = (count: unknown) => {
    const parsed = Number(count);
    return Number.isFinite(parsed) && parsed > 0 ? Math.max(1, Math.min(12, Math.round(parsed))) : 6;
};

type NavItem = {
    key: TabKey;
    label: string;
    icon: React.ElementType;
};

type SettingRowProps = {
    title: string;
    description?: string;
    control: React.ReactNode;
    stacked?: boolean;
    disabled?: boolean;
};

const NAV_ITEMS: NavItem[] = [
    { key: 'general', label: TEXT.general, icon: Settings },
    { key: 'scan', label: TEXT.scan, icon: Search },
    { key: 'remote', label: TEXT.remoteAccess, icon: Server },
    { key: 'about', label: TEXT.about, icon: Info },
];

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
    title,
    description,
    control,
    stacked = false,
    disabled = false,
}) => (
    <div className={`settings-row-card ${stacked ? 'is-stacked' : ''} ${disabled ? 'is-disabled' : ''}`.trim()}>
        <div className="settings-row-copy">
            <div className="settings-row-title">{title}</div>
            {description && <div className="settings-row-description">{description}</div>}
        </div>
        <div className={`settings-row-control ${stacked ? 'is-stacked' : ''}`}>{control}</div>
    </div>
);

const SettingsGroup: React.FC<{ title: string; children: React.ReactNode }> = ({ title, children }) => (
    <section className="settings-group">
        <h3 className="settings-group-title">{title}</h3>
        <div className="settings-panel">{children}</div>
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
                if (res) {
                    const nextSettings = cloneSettings(res);
                    setSettings(nextSettings);
                    setSavedSettings(cloneSettings(nextSettings));
                }
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
            window.setTimeout(clearFeedback, 2500);
        } catch (error) {
            setMsg(`${TEXT.saveFailed}：${formatError(error)}`);
            setFeedbackTone('error');
        }
    };

    const handleDiscard = () => {
        if (savedSettings) {
            setSettings(cloneSettings(savedSettings));
        }
        clearFeedback();
    };

    const handleSelectProgram = async () => {
        try {
            const programPath = await SelectProgram();
            if (programPath) {
                updateSettings({ player_path: programPath });
            }
        } catch (error) {
            setMsg(`${TEXT.selectProgramFailed}：${formatError(error)}`);
            setFeedbackTone('error');
        }
    };

    if (!settings) {
        return <div className="settings-loading">{TEXT.loading}</div>;
    }

    const bindHost = settings.remote_bind_host || '0.0.0.0';
    const jellyfinPort = Number(settings.jellyfin_port) > 0 ? Number(settings.jellyfin_port) : 18096;
    const jellyfinURL = `http://${bindHost}:${jellyfinPort}`;

    const renderGeneral = () => (
        <>
            <SettingsGroup title="启动与窗口">
                <SettingRow
                    title={TEXT.startup}
                    description={TEXT.startupDesc}
                    control={<SettingSwitch checked={!!settings.start_with_os} onChange={(checked) => updateSettings({ start_with_os: checked })} />}
                />
                <SettingRow
                    title={TEXT.tray}
                    description={TEXT.trayDesc}
                    control={<SettingSwitch checked={!!settings.min_to_tray} onChange={(checked) => updateSettings({ min_to_tray: checked })} />}
                />
            </SettingsGroup>

            <SettingsGroup title="播放">
                <SettingRow
                    title={TEXT.externalPlayer}
                    description={TEXT.externalPlayerDesc}
                    control={<SettingSwitch checked={!!settings.use_external_player} onChange={(checked) => updateSettings({ use_external_player: checked })} />}
                />
                <SettingRow
                    title={TEXT.externalPlayerName}
                    description={TEXT.externalPlayerHint}
                    stacked
                    disabled={!settings.use_external_player}
                    control={(
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
                                <button className="settings-secondary-button settings-secondary-button-primary" type="button" onClick={handleSelectProgram} disabled={!settings.use_external_player}>
                                    {TEXT.chooseProgram}
                                </button>
                                <button className="settings-secondary-button settings-secondary-button-clear" type="button" onClick={() => updateSettings({ player_path: '' })} disabled={!settings.use_external_player || !settings.player_path}>
                                    {TEXT.clear}
                                </button>
                            </div>
                        </div>
                    )}
                />
            </SettingsGroup>
        </>
    );

    const renderScan = () => (
        <>
            <SettingsGroup title="扫描来源">
                <SettingRow
                    title={TEXT.useEverything}
                    description={TEXT.useEverythingDesc}
                    control={<SettingSwitch checked={!!settings.use_everything} onChange={(checked) => updateSettings({ use_everything: checked })} />}
                />
                <SettingRow
                    title={TEXT.everythingAddr}
                    description={TEXT.everythingAddrDesc}
                    stacked
                    disabled={!settings.use_everything}
                    control={(
                        <input
                            className="settings-input"
                            type="text"
                            value={settings.everything_addr || ''}
                            placeholder={TEXT.everythingAddrPlaceholder}
                            onChange={(event) => updateSettings({ everything_addr: event.target.value })}
                            disabled={!settings.use_everything}
                        />
                    )}
                />
                <div className="settings-note-row">{TEXT.everythingTip}</div>
            </SettingsGroup>

            <SettingsGroup title="媒体补全">
                <SettingRow
                    title={TEXT.videoThumbnail}
                    description={TEXT.videoThumbnailDesc}
                    control={<SettingSwitch checked={!!settings.enable_video_thumbnail} onChange={(checked) => updateSettings({ enable_video_thumbnail: checked })} />}
                />
                <SettingRow
                    title={TEXT.gfriendsAvatars}
                    description={TEXT.gfriendsAvatarsDesc}
                    control={<SettingSwitch checked={!!settings.enable_gfriends_avatars} onChange={(checked) => updateSettings({ enable_gfriends_avatars: checked })} />}
                />
                <SettingRow
                    title={TEXT.thumbnailMinDuration}
                    description={TEXT.thumbnailMinDurationDesc}
                    disabled={!settings.enable_video_thumbnail}
                    control={(
                        <input
                            className="settings-input settings-number-input"
                            type="number"
                            min={1}
                            step={1}
                            value={getThumbnailDurationMinutes(settings.thumbnail_min_duration_seconds)}
                            onChange={(event) => updateSettings({ thumbnail_min_duration_seconds: Math.max(1, Number(event.target.value) || 0) * 60 })}
                            disabled={!settings.enable_video_thumbnail}
                        />
                    )}
                />
                <SettingRow
                    title={TEXT.thumbnailPreviewCount}
                    description={TEXT.thumbnailPreviewCountDesc}
                    disabled={!settings.enable_video_thumbnail}
                    control={(
                        <input
                            className="settings-input settings-number-input"
                            type="number"
                            min={1}
                            max={12}
                            step={1}
                            value={getThumbnailPreviewCount(settings.thumbnail_preview_count)}
                            onChange={(event) => updateSettings({ thumbnail_preview_count: Math.max(1, Math.min(12, Number(event.target.value) || 0)) })}
                            disabled={!settings.enable_video_thumbnail}
                        />
                    )}
                />
            </SettingsGroup>
        </>
    );

    const renderRemote = () => (
        <>
            <SettingsGroup title="远程访问">
                <SettingRow
                    title={TEXT.remoteBindHost}
                    description={TEXT.remoteBindHostDesc}
                    stacked
                    control={<input className="settings-input" type="text" value={settings.remote_bind_host || ''} placeholder="0.0.0.0" onChange={(event) => updateSettings({ remote_bind_host: event.target.value })} />}
                />
                <SettingRow
                    title={TEXT.remoteUsername}
                    description={TEXT.remoteUsernameDesc}
                    stacked
                    control={<input className="settings-input" type="text" value={settings.remote_username || ''} onChange={(event) => updateSettings({ remote_username: event.target.value })} />}
                />
                <SettingRow
                    title={TEXT.remotePassword}
                    description={TEXT.remotePasswordDesc}
                    stacked
                    control={(
                        <div className="settings-password-shell">
                            <input className="settings-input settings-password-input" type={showPassword ? 'text' : 'password'} value={settings.remote_password || ''} onChange={(event) => updateSettings({ remote_password: event.target.value })} />
                            <button type="button" className="settings-password-toggle" onClick={() => setShowPassword((visible) => !visible)} aria-label={showPassword ? TEXT.hidePassword : TEXT.showPassword} title={showPassword ? TEXT.hidePassword : TEXT.showPassword}>
                                {showPassword ? <EyeOff size={15} /> : <Eye size={15} />}
                            </button>
                        </div>
                    )}
                />
            </SettingsGroup>

            <SettingsGroup title="Jellyfin Sidecar">
                <SettingRow
                    title={TEXT.jellyfinEnabled}
                    description={TEXT.jellyfinEnabledDesc}
                    control={<SettingSwitch checked={!!settings.jellyfin_enabled} onChange={(checked) => updateSettings({ jellyfin_enabled: checked })} />}
                />
                <SettingRow
                    title={TEXT.jellyfinPort}
                    description={TEXT.jellyfinPortDesc}
                    stacked
                    disabled={!settings.jellyfin_enabled}
                    control={<input className="settings-input" type="number" min={1} max={65535} step={1} value={jellyfinPort} onChange={(event) => updateSettings({ jellyfin_port: Math.max(1, Number(event.target.value) || 0) })} disabled={!settings.jellyfin_enabled} />}
                />
                <SettingRow
                    title={TEXT.jellyfinServerName}
                    description={TEXT.jellyfinServerNameDesc}
                    stacked
                    disabled={!settings.jellyfin_enabled}
                    control={<input className="settings-input" type="text" value={settings.jellyfin_server_name || ''} onChange={(event) => updateSettings({ jellyfin_server_name: event.target.value })} disabled={!settings.jellyfin_enabled} />}
                />
            </SettingsGroup>

            <SettingsGroup title="服务地址">
                <div className="settings-address-card">
                    <div className="settings-address-value">{jellyfinURL}</div>
                    <div className="settings-row-description">{TEXT.remoteLanHint}</div>
                </div>
            </SettingsGroup>
        </>
    );

    const renderAbout = () => (
        <SettingsGroup title="关于 Navi">
            <div className="settings-about-panel">
                <div className="settings-about-title">{TEXT.aboutCardTitle}</div>
                <div className="settings-about-description">{TEXT.aboutCardDesc}</div>
                <div className="settings-about-description">{TEXT.aboutCardNote}</div>
            </div>
        </SettingsGroup>
    );

    const renderContent = () => {
        if (activeTab === 'general') return renderGeneral();
        if (activeTab === 'scan') return renderScan();
        if (activeTab === 'remote') return renderRemote();
        return renderAbout();
    };

    const statusText = msg || (hasChanges ? TEXT.unsaved : TEXT.synced);

    return (
        <div className="settings-container">
            <div className="settings-layout">
                <aside className="settings-sidebar">
                    <nav className="settings-nav-list" aria-label="设置分类">
                        {NAV_ITEMS.map((item) => {
                            const Icon = item.icon;
                            const isActive = activeTab === item.key;
                            return (
                                <button key={item.key} type="button" className={`settings-nav-item ${isActive ? 'active' : ''}`} onClick={() => setActiveTab(item.key)}>
                                    <Icon size={15} strokeWidth={1.8} aria-hidden="true" />
                                    <span>{item.label}</span>
                                </button>
                            );
                        })}
                    </nav>
                </aside>

                <main className="settings-main">
                    <div className="settings-main-scroll">{renderContent()}</div>
                    <footer className="settings-page-footer">
                        <div className={`settings-footer-status ${feedbackTone ? `is-${feedbackTone}` : ''}`} aria-live="polite">
                            {hasChanges && !msg && <span className="settings-status-dot" />}
                            <span>{statusText}</span>
                        </div>
                        <div className="settings-page-footer-actions">
                            <button className="settings-secondary-button" type="button" onClick={handleDiscard} disabled={!hasChanges}> {TEXT.discard} </button>
                            <button className="settings-primary-button" type="button" onClick={handleSave} disabled={!hasChanges}> {TEXT.save} </button>
                        </div>
                    </footer>
                </main>
            </div>
        </div>
    );
};

export default SettingsPage;
