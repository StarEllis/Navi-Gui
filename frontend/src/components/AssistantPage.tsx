import React, { useEffect, useRef, useState, useSyncExternalStore } from 'react';
import {
    Brain,
    Check,
    ChevronDown,
    ChevronRight,
    Database,
    Eye,
    Film,
    FolderOpen,
    Images,
    LoaderCircle,
    MessageSquare,
    PanelLeftClose,
    PanelLeftOpen,
    Pencil,
    Play,
    Plus,
    SendHorizontal,
    Settings2,
    ShieldAlert,
    ShieldCheck,
    Sparkles,
    Square,
    Trash2,
    X,
} from 'lucide-react';
import {
    AssistantCurrentConversation,
    AssistantDecide,
    AssistantDeleteConversation,
    AssistantListConversations,
    AssistantNewConversation,
    AssistantOpenConversation,
    AssistantPendingProposal,
    AssistantSend,
    AssistantStop,
    GetAssistantSettings,
    SaveAssistantSettings,
} from '../../wailsjs/go/main/App';
import { assistant } from '../../wailsjs/go/models';
import { EventsOn } from '../../wailsjs/runtime/runtime';
import MediaCard from './MediaCard';
import AssistantSettingsModal from './AssistantSettingsModal';
import MiniMarkdown from '../utils/miniMarkdown';
import { toLocalAssetUrl } from '../utils/media';
import type { StatusKind } from '../types/status';
import '../assistant.css';

// AboutMedia 是从详情页带过来的那部片，下一句话就是问它的。
type AboutMedia = {
    id: string;
    code: string;
    title: string;
    poster_path: string;
    updated_at: string;
};

type ChatState = {
    conversation: assistant.Conversation | null;
    list: assistant.ConversationSummary[];
    pendingText: string; // 非空表示正在等回答
    notice: string; // 没进会话的错误，比如上一句还没完
    proposal: assistant.Proposal | null; // 正在等你确认的修改
    deciding: boolean; // 已经点了执行或取消，等结果
    liveSteps: assistant.Step[]; // 正在处理的这句话已经走过的步骤
    about: AboutMedia | null; // 输入框上挂着的那部片
    pendingAbout: AboutMedia | null; // 正在等回答的那句话问的是哪部片
};

// 带着一部片来问时，输入框上方给的几个现成问法。
const ABOUT_PROMPTS = ['找几部类似的', '这个演员还有哪些我没看过的', '同系列的其他片', '用几句话介绍一下这部'];

const EXAMPLES = [
    'S1 的 4K 片子有多少部？',
    '哪个片商的片子我收得最多？',
    '还没看过的里面，评分最高的 10 部',
    '女上司被下属反攻的片子',
];

// 会话状态放在模块里而不是组件里：切到别的页面再回来还在，
// 请求没回来就切走，结果也照样落进来。
let chatState: ChatState = {
    conversation: null, list: [], pendingText: '', notice: '', proposal: null, deciding: false, liveSteps: [], about: null, pendingAbout: null,
};
const chatListeners = new Set<() => void>();

const setChatState = (update: Partial<ChatState> | ((state: ChatState) => Partial<ChatState>)) => {
    chatState = { ...chatState, ...(typeof update === 'function' ? update(chatState) : update) };
    chatListeners.forEach((listener) => listener());
};

const subscribeChat = (listener: () => void) => {
    chatListeners.add(listener);
    return () => {
        chatListeners.delete(listener);
    };
};

const formatError = (error: unknown) => {
    if (error instanceof Error && error.message) return error.message;
    if (typeof error === 'string' && error) return error;
    return '未知错误';
};

const refreshList = async () => {
    try {
        setChatState({ list: (await AssistantListConversations()) || [] });
    } catch (error) {
        setChatState({ notice: `读取会话列表失败：${formatError(error)}` });
    }
};

// 进度和确认卡片的事件也在模块里收：切到别的页面时照样记下来，回来能看到。
let listeningToSession = false;
const listenToSession = () => {
    if (listeningToSession) return;
    listeningToSession = true;
    EventsOn('assistant-step', (step: assistant.Step) => {
        setChatState((state) => ({
            liveSteps: [...state.liveSteps, step],
            ...(step.tool === 'propose_change' ? { proposal: null, deciding: false } : {}),
        }));
    });
    EventsOn('assistant-confirm', (next: assistant.Proposal) => {
        setChatState({ proposal: next, deciding: false });
    });
};

// askAssistantAbout 是详情页"问助手"按钮：把这部片挂到输入框上，接着在当前会话里问。
export const askAssistantAbout = (media: { id: string; code?: string; title?: string; poster_path?: string; updated_at?: string }) => {
    setChatState({
        about: {
            id: media.id,
            code: media.code || '',
            title: media.title || '',
            poster_path: media.poster_path || '',
            updated_at: String(media.updated_at || ''),
        },
        notice: '',
    });
};

const sendMessage = async (text: string) => {
    if (chatState.pendingText) return;
    const about = chatState.about;
    listenToSession();
    setChatState({ pendingText: text, pendingAbout: about, about: null, notice: '', liveSteps: [] });
    try {
        const conversation = await AssistantSend(text, about?.id || '');
        setChatState({ conversation });
        void refreshList();
    } catch (error) {
        // 没发出去（比如上一句还没完）：把片子挂回输入框，不用再去详情页点一次。
        setChatState((state) => ({ notice: formatError(error), about: state.about || about }));
    } finally {
        setChatState({ pendingText: '', pendingAbout: null, proposal: null, deciding: false, liveSteps: [] });
    }
};

const posterURL = (media: { poster_path?: string; updated_at?: string }) =>
    media.poster_path ? `${toLocalAssetUrl(media.poster_path)}?v=${encodeURIComponent(String(media.updated_at || ''))}` : '';

// AboutPoster 是小号海报，缺封面时显示一个胶片图标。
const AboutPoster: React.FC<{ media: { poster_path?: string; updated_at?: string } }> = ({ media }) => {
    const [failed, setFailed] = useState(false);
    const url = posterURL(media);
    return (
        <span className="asst-about-poster">
            {url && !failed ? <img src={url} alt="" onError={() => setFailed(true)} /> : <Film size={14} />}
        </span>
    );
};

// QuotedMedia 挂在用户那句话上方，说明这句话问的是哪部片，点了打开详情。
const QuotedMedia: React.FC<{ media: AboutMedia | assistant.MediaCard; onOpenMedia?: (mediaID: string) => void }> = ({ media, onOpenMedia }) => (
    <button
        type="button"
        className="asst-quote"
        title="打开这部片"
        disabled={!onOpenMedia}
        onClick={() => onOpenMedia?.(media.id)}
    >
        <AboutPoster media={media} />
        <span className="asst-quote-text">
            {media.code && <b>{media.code}</b>}
            <span>{media.title}</span>
        </span>
    </button>
);

const decideProposal = async (approve: boolean) => {
    const proposal = chatState.proposal;
    if (!proposal || chatState.deciding) return;
    setChatState({ deciding: true });
    try {
        await AssistantDecide(proposal.id, approve);
    } catch (error) {
        setChatState({ proposal: null, deciding: false, notice: formatError(error) });
    }
};

const startNewConversation = async () => {
    try {
        await AssistantNewConversation();
        setChatState({ conversation: null, notice: '' });
    } catch (error) {
        setChatState({ notice: formatError(error) });
    }
};

const openConversation = async (id: string) => {
    try {
        setChatState({ conversation: await AssistantOpenConversation(id), notice: '' });
    } catch (error) {
        setChatState({ notice: formatError(error) });
        void refreshList();
    }
};

const deleteConversation = async (id: string) => {
    try {
        await AssistantDeleteConversation(id);
        if (chatState.conversation?.id === id) setChatState({ conversation: null });
        await refreshList();
    } catch (error) {
        setChatState({ notice: formatError(error) });
    }
};

// 卡片上点了收藏、打了分，把新状态写回对应的那张卡。
const updateCardMedia = (media: any) => {
    setChatState((state) => {
        if (!state.conversation) return {};
        const items = state.conversation.items.map((item) => ({
            ...item,
            media: item.media?.map((card) => (card.id === media.id ? { ...card, ...media } : card)),
        }));
        return { conversation: { ...state.conversation, items } as assistant.Conversation };
    });
};

const formatTime = (ms: number) => {
    if (!ms) return '';
    const date = new Date(ms);
    const diff = Date.now() - ms;
    if (diff < 60_000) return '刚刚';
    if (diff < 3_600_000) return `${Math.floor(diff / 60_000)} 分钟前`;
    const pad = (n: number) => String(n).padStart(2, '0');
    const sameDay = new Date().toDateString() === date.toDateString();
    return sameDay
        ? `今天 ${pad(date.getHours())}:${pad(date.getMinutes())}`
        : `${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
};

const stepIcon = (tool: string) => {
    if (tool === 'thinking') return <Brain size={13} />;
    if (tool === 'propose_change') return <Pencil size={13} />;
    if (tool === 'open_folder') return <FolderOpen size={13} />;
    if (tool === 'play_media') return <Play size={13} />;
    return tool === 'show_media' ? <Images size={13} /> : <Database size={13} />;
};
const stepLabel = (tool: string) => {
    if (tool === 'thinking') return '思考';
    if (tool === 'propose_change') return '修改';
    if (tool === 'open_folder') return '打开文件夹';
    if (tool === 'play_media') return '播放';
    return tool === 'show_media' ? '展示影片' : '查询数据库';
};

const StepTimeline: React.FC<{ steps: assistant.Step[]; live?: boolean }> = ({ steps, live = false }) => (
    <ol className="asst-steps">
        {steps.map((step, index) => (
            <li key={index} className={`${step.error ? 'error' : ''} ${step.tool === 'thinking' ? 'thinking' : ''}`.trim()}>
                <div className="asst-step-row">
                    <span className="asst-step-icon">{stepIcon(step.tool)}</span>
                    <span className="asst-step-label">{stepLabel(step.tool)}</span>
                    <span className="asst-step-summary">{step.error ? '出错' : step.summary}</span>
                </div>
                {step.tool === 'thinking' && step.input && <div className="asst-step-thought">{step.input}</div>}
                {step.tool === 'run_sql' && step.input && <pre className="asst-step-sql">{step.input}</pre>}
                {step.tool === 'propose_change' && step.input && <div className="asst-step-thought">{step.input}</div>}
                {step.error && <div className="asst-step-error">{step.error}</div>}
            </li>
        ))}
        {live && (
            <li className="live">
                <div className="asst-step-row">
                    <span className="asst-step-icon"><LoaderCircle size={13} className="spin" /></span>
                    <span className="asst-step-label">{steps.length === 0 ? '正在思考' : '继续查询'}</span>
                </div>
            </li>
        )}
    </ol>
);

const StepSummary: React.FC<{ steps: assistant.Step[] }> = ({ steps }) => {
    const [open, setOpen] = useState(false);
    const queries = steps.filter((step) => step.tool === 'run_sql').length;
    const errors = steps.filter((step) => step.error).length;
    const parts = ['思考过程'];
    if (queries > 0) parts.push(`查询 ${queries} 次`);
    if (errors > 0) parts.push(`${errors} 次出错`);
    return (
        <div className="asst-step-summary-box">
            <button type="button" className="asst-step-toggle" onClick={() => setOpen((value) => !value)}>
                {open ? <ChevronDown size={13} /> : <ChevronRight size={13} />}
                <Brain size={12} />
                {parts.join(' · ')}
            </button>
            {open && <StepTimeline steps={steps} />}
        </div>
    );
};

// 找到的影片默认展开，片多的时候可以收起来只看文字
const MediaSection: React.FC<{
    media: assistant.MediaCard[];
    onOpenMedia: (mediaID: string) => void;
    onStatus: (message: string, kind?: StatusKind) => void;
}> = ({ media, onOpenMedia, onStatus }) => {
    const [open, setOpen] = useState(true);
    return (
        <div className="asst-media-section">
            <button type="button" className="asst-step-toggle" onClick={() => setOpen((value) => !value)}>
                {open ? <ChevronDown size={13} /> : <ChevronRight size={13} />}
                <Images size={12} />
                找到的影片 · {media.length} 部
            </button>
            {open && (
                <div className="asst-media-grid">
                    {media.map((card) => (
                        <MediaCard
                            key={card.id}
                            media={card}
                            onSelectMedia={(selected: any) => onOpenMedia(selected.id)}
                            onQuickPlayStatus={onStatus}
                            onMediaChange={updateCardMedia}
                        />
                    ))}
                </div>
            )}
        </div>
    );
};

const PROPOSAL_STATUS: Record<string, string> = {
    pending: '等你确认',
    applied: '已执行',
    cancelled: '没有执行',
    failed: '执行出错',
};
const PROPOSAL_PREVIEW_CARDS = 12;

// 修改的确认卡片。live 时带执行、取消按钮和要改的片子；执行完留在会话里只显示结果。
const ProposalCard: React.FC<{
    proposal: assistant.Proposal;
    live?: boolean;
    deciding?: boolean;
    onOpenMedia: (mediaID: string) => void;
    onStatus: (message: string, kind?: StatusKind) => void;
}> = ({ proposal, live = false, deciding = false, onOpenMedia, onStatus }) => {
    const media = proposal.media || [];
    const lines = proposal.lines || [];
    return (
        <div className={`asst-proposal ${proposal.danger ? 'danger' : ''} ${live ? 'live' : proposal.status}`.trim()}>
            <div className="asst-proposal-head">
                <Pencil size={13} />
                <span className="asst-proposal-title">{proposal.title}</span>
                {!live && (
                    <span className="asst-proposal-status">
                        {proposal.auto && proposal.status === 'applied' ? '已自动执行' : PROPOSAL_STATUS[proposal.status] || proposal.status}
                    </span>
                )}
            </div>
            {live && lines.length > 0 && (
                <ul className="asst-proposal-lines">
                    {lines.map((line, index) => <li key={index}>{line}</li>)}
                </ul>
            )}
            {live && media.length > 0 && (
                <>
                    <div className="asst-media-grid">
                        {media.slice(0, PROPOSAL_PREVIEW_CARDS).map((card) => (
                            <MediaCard
                                key={card.id}
                                media={card}
                                onSelectMedia={(selected: any) => onOpenMedia(selected.id)}
                                onQuickPlayStatus={onStatus}
                                onMediaChange={updateCardMedia}
                            />
                        ))}
                    </div>
                    {media.length > PROPOSAL_PREVIEW_CARDS && (
                        <div className="asst-proposal-more">还有 {media.length - PROPOSAL_PREVIEW_CARDS} 部没列出来</div>
                    )}
                </>
            )}
            {!live && proposal.status !== 'cancelled' && proposal.result && (
                <div className="asst-proposal-result">{proposal.result}</div>
            )}
            {live && (
                <div className="asst-proposal-actions">
                    <button
                        type="button"
                        className={`asst-btn ${proposal.danger ? 'danger' : 'primary'}`}
                        disabled={deciding}
                        onClick={() => void decideProposal(true)}
                    >
                        {deciding ? '处理中…' : '执行'}
                    </button>
                    <button type="button" className="asst-btn" disabled={deciding} onClick={() => void decideProposal(false)}>
                        取消
                    </button>
                </div>
            )}
        </div>
    );
};

const EFFORT_OPTIONS = [
    { value: '', label: '默认', hint: '不传参数，用模型自己的设定' },
    { value: 'low', label: '低', hint: '快、省，适合数数和简单筛选' },
    { value: 'medium', label: '中', hint: '一般问题够用' },
    { value: 'high', label: '高', hint: '按剧情找片、复杂条件时更准，但更慢' },
];

const SIDEBAR_KEY = 'navi.assistant.sidebarCollapsed';

const loadSidebarCollapsed = () => {
    try {
        return window.localStorage.getItem(SIDEBAR_KEY) === '1';
    } catch {
        return false;
    }
};

const saveSidebarCollapsed = (collapsed: boolean) => {
    try {
        window.localStorage.setItem(SIDEBAR_KEY, collapsed ? '1' : '0');
    } catch {
        // 存不了就只在这次打开时有效
    }
};

// 点外面就收起的小弹出菜单
const usePopover = () => {
    const [open, setOpen] = useState(false);
    const rootRef = useRef<HTMLDivElement | null>(null);
    useEffect(() => {
        if (!open) return;
        const close = (event: MouseEvent) => {
            if (!rootRef.current?.contains(event.target as Node)) setOpen(false);
        };
        window.addEventListener('mousedown', close);
        return () => window.removeEventListener('mousedown', close);
    }, [open]);
    return { open, setOpen, rootRef };
};

const EffortPicker: React.FC<{
    value: string;
    disabled: boolean;
    onPick: (effort: string) => void;
}> = ({ value, disabled, onPick }) => {
    const { open, setOpen, rootRef } = usePopover();
    const current = EFFORT_OPTIONS.find((option) => option.value === value) || EFFORT_OPTIONS[0];
    return (
        <div className="asst-picker" ref={rootRef}>
            <button
                type="button"
                className="asst-picker-trigger"
                disabled={disabled}
                title="推理强度"
                onClick={() => setOpen((show) => !show)}
            >
                <Brain size={13} />
                <span>推理 · {current.label}</span>
                <ChevronDown size={13} />
            </button>
            {open && (
                <div className="asst-picker-menu narrow" role="menu">
                    <div className="asst-picker-group-title">推理强度</div>
                    {EFFORT_OPTIONS.map((option) => (
                        <button
                            key={option.value || 'default'}
                            type="button"
                            className={`asst-picker-item ${option.value === value ? 'active' : ''}`.trim()}
                            onClick={() => {
                                setOpen(false);
                                onPick(option.value);
                            }}
                        >
                            <span className="asst-effort-option">
                                <strong>{option.label}</strong>
                                <small>{option.hint}</small>
                            </span>
                            {option.value === value && <Check size={13} />}
                        </button>
                    ))}
                </div>
            )}
        </div>
    );
};

const PERMISSION_OPTIONS = [
    { value: 'readonly', label: '仅可查看', hint: '只能查，不能改任何东西', foot: '仅可查看 · 只能查，不能改', icon: Eye },
    { value: 'edit', label: '可以修改', hint: '每次修改都先让你确认', foot: '要改东西时，会先让你确认', icon: ShieldCheck },
    {
        value: 'full',
        label: '完全权限',
        hint: '小改动直接做，还能删视频文件；改信息、移除、删文件仍要你确认',
        foot: '完全权限 · 小改动直接做，删文件仍要确认',
        icon: ShieldAlert,
    },
];

const permissionOption = (value: string | undefined) =>
    PERMISSION_OPTIONS.find((option) => option.value === value) || PERMISSION_OPTIONS[1];

const PermissionPicker: React.FC<{
    value: string;
    disabled: boolean;
    onPick: (permission: string) => void;
}> = ({ value, disabled, onPick }) => {
    const { open, setOpen, rootRef } = usePopover();
    const current = permissionOption(value);
    const Icon = current.icon;
    return (
        <div className="asst-picker" ref={rootRef}>
            <button
                type="button"
                className={`asst-picker-trigger ${current.value === 'full' ? 'warn' : ''}`.trim()}
                disabled={disabled}
                title="助手的权限"
                onClick={() => setOpen((show) => !show)}
            >
                <Icon size={13} />
                <span>{current.label}</span>
                <ChevronDown size={13} />
            </button>
            {open && (
                <div className="asst-picker-menu narrow" role="menu">
                    <div className="asst-picker-group-title">权限</div>
                    {PERMISSION_OPTIONS.map((option) => {
                        const OptionIcon = option.icon;
                        return (
                            <button
                                key={option.value}
                                type="button"
                                className={`asst-picker-item ${option.value === current.value ? 'active' : ''}`.trim()}
                                onClick={() => {
                                    setOpen(false);
                                    onPick(option.value);
                                }}
                            >
                                <span className="asst-effort-option">
                                    <strong><OptionIcon size={12} /> {option.label}</strong>
                                    <small>{option.hint}</small>
                                </span>
                                {option.value === current.value && <Check size={13} />}
                            </button>
                        );
                    })}
                </div>
            )}
        </div>
    );
};

const ModelPicker: React.FC<{
    settings: assistant.Settings | null;
    disabled: boolean;
    onPick: (presetID: string, model: string) => void;
    onManage: () => void;
}> = ({ settings, disabled, onPick, onManage }) => {
    const { open, setOpen, rootRef } = usePopover();

    const presets = settings?.presets || [];
    const active = presets.find((p) => p.id === settings?.active_preset);
    const label = active ? `${active.name} · ${settings?.active_model || '未选模型'}` : '未设置接口';

    return (
        <div className="asst-picker" ref={rootRef}>
            <button
                type="button"
                className="asst-picker-trigger"
                disabled={disabled}
                title="切换预设和模型"
                onClick={() => (presets.length === 0 ? onManage() : setOpen((value) => !value))}
            >
                <Sparkles size={13} />
                <span>{label}</span>
                <ChevronDown size={13} />
            </button>
            {open && (
                <div className="asst-picker-menu" role="menu">
                    {presets.map((preset) => (
                        <div key={preset.id} className="asst-picker-group">
                            <div className="asst-picker-group-title">{preset.name}</div>
                            {preset.models.length === 0 && <div className="asst-picker-empty">没有模型</div>}
                            {preset.models.map((model) => {
                                const current = preset.id === settings?.active_preset && model === settings?.active_model;
                                return (
                                    <button
                                        key={model}
                                        type="button"
                                        className={`asst-picker-item ${current ? 'active' : ''}`.trim()}
                                        onClick={() => {
                                            setOpen(false);
                                            onPick(preset.id, model);
                                        }}
                                    >
                                        <span>{model}</span>
                                        {current && <Check size={13} />}
                                    </button>
                                );
                            })}
                        </div>
                    ))}
                    <button
                        type="button"
                        className="asst-picker-manage"
                        onClick={() => {
                            setOpen(false);
                            onManage();
                        }}
                    >
                        <Settings2 size={13} />
                        管理接口预设
                    </button>
                </div>
            )}
        </div>
    );
};

interface AssistantPageProps {
    onOpenMedia: (mediaID: string) => void;
    onStatus: (message: string, kind?: StatusKind) => void;
}

const AssistantPage: React.FC<AssistantPageProps> = ({ onOpenMedia, onStatus }) => {
    const { conversation, list, pendingText, notice, proposal, deciding, liveSteps, about, pendingAbout } = useSyncExternalStore(subscribeChat, () => chatState);
    const pending = pendingText !== '';
    const [input, setInput] = useState('');
    const [settings, setSettings] = useState<assistant.Settings | null>(null);
    const [settingsOpen, setSettingsOpen] = useState(false);
    const [confirmDeleteID, setConfirmDeleteID] = useState('');
    const [sidebarCollapsed, setSidebarCollapsed] = useState(loadSidebarCollapsed);

    const toggleSidebar = () => {
        setSidebarCollapsed((collapsed) => {
            saveSidebarCollapsed(!collapsed);
            return !collapsed;
        });
    };
    const scrollRef = useRef<HTMLDivElement | null>(null);

    useEffect(() => {
        GetAssistantSettings()
            .then((loaded) => {
                setSettings(loaded);
                // 一个预设都没有时直接打开设置，不然第一句话必然报错
                if (!loaded.presets?.length) setSettingsOpen(true);
            })
            .catch((error: unknown) => onStatus(`读取助手设置失败：${formatError(error)}`, 'error'));
        void refreshList();
        // 切到别的页面时收不到确认卡片的事件，回来时补一下。
        if (chatState.pendingText && !chatState.proposal) {
            AssistantPendingProposal()
                .then((current) => {
                    if (current && chatState.pendingText) setChatState({ proposal: current });
                })
                .catch(() => undefined);
        }
        if (!chatState.conversation && !chatState.pendingText) {
            AssistantCurrentConversation()
                .then((current) => {
                    if (current) setChatState({ conversation: current });
                })
                .catch(() => undefined);
        }
    }, [onStatus]);

    useEffect(() => {
        const node = scrollRef.current;
        if (node) node.scrollTop = node.scrollHeight;
    }, [conversation?.id, conversation?.items.length, pending, liveSteps.length, proposal?.id]);

    // 从详情页带着片子过来：光标直接落在输入框里。
    const inputRef = useRef<HTMLTextAreaElement | null>(null);
    useEffect(() => {
        if (about) inputRef.current?.focus();
    }, [about?.id]);

    const submit = (text: string) => {
        const trimmed = text.trim();
        if (!trimmed || pending) return;
        setInput('');
        void sendMessage(trimmed);
    };

    const saveSettingsPatch = async (patch: Partial<assistant.Settings>) => {
        if (!settings) return;
        try {
            setSettings(await SaveAssistantSettings(assistant.Settings.createFrom({ ...settings, ...patch })));
        } catch (error) {
            onStatus(`保存失败：${formatError(error)}`, 'error');
        }
    };

    const items = conversation?.items || [];
    const showEmpty = items.length === 0 && !pending;

    return (
        <div className="asst-page">
            <aside className={`asst-sidebar ${sidebarCollapsed ? 'collapsed' : ''}`.trim()}>
                <div className="asst-sidebar-head">
                    <button
                        type="button"
                        className="asst-icon-btn"
                        title={sidebarCollapsed ? '展开会话栏' : '收起会话栏'}
                        onClick={toggleSidebar}
                    >
                        {sidebarCollapsed ? <PanelLeftOpen size={16} /> : <PanelLeftClose size={16} />}
                    </button>
                    {!sidebarCollapsed && <span className="asst-sidebar-title">会话</span>}
                </div>
                <button
                    type="button"
                    className="asst-new-btn"
                    disabled={pending}
                    title="新对话"
                    onClick={() => void startNewConversation()}
                >
                    <Plus size={15} />
                    {!sidebarCollapsed && '新对话'}
                </button>
                {!sidebarCollapsed && <div className="asst-conv-list">
                    {list.length === 0 && <div className="asst-conv-empty">还没有会话</div>}
                    {list.map((item) => {
                        const active = item.id === conversation?.id;
                        const confirming = confirmDeleteID === item.id;
                        return (
                            <div
                                key={item.id}
                                className={`asst-conv ${active ? 'active' : ''} ${confirming ? 'confirming' : ''}`.trim()}
                                onMouseLeave={() => confirming && setConfirmDeleteID('')}
                            >
                                <button
                                    type="button"
                                    className="asst-conv-main"
                                    disabled={pending}
                                    onClick={() => void openConversation(item.id)}
                                >
                                    <MessageSquare size={13} />
                                    <span className="asst-conv-title">{item.title || '未命名会话'}</span>
                                    <span className="asst-conv-time">{formatTime(item.updated_at)}</span>
                                </button>
                                {confirming ? (
                                    <button
                                        type="button"
                                        className="asst-conv-confirm"
                                        onClick={() => {
                                            setConfirmDeleteID('');
                                            void deleteConversation(item.id);
                                        }}
                                    >
                                        删除
                                    </button>
                                ) : (
                                    <button
                                        type="button"
                                        className="asst-conv-delete"
                                        title="删除会话"
                                        disabled={pending && active}
                                        onClick={() => setConfirmDeleteID(item.id)}
                                    >
                                        <Trash2 size={13} />
                                    </button>
                                )}
                            </div>
                        );
                    })}
                </div>}
                {!sidebarCollapsed && <div className="asst-sidebar-foot">{permissionOption(settings?.permission).foot}</div>}
            </aside>

            <section className="asst-main">
                <div className="asst-scroll" ref={scrollRef}>
                    <div className="asst-thread">
                        {showEmpty && (
                            <div className="asst-empty">
                                <div className="asst-empty-icon"><Sparkles size={20} /></div>
                                <h2>{about ? '问问这部片' : '用一句话找片'}</h2>
                                <p>
                                    {about
                                        ? '找类似的、查同演员或同系列的片，也可以让它收藏、打标签。下面有几个现成的问法。'
                                        : '描述条件、剧情或者想看的统计，助手会去库里查，再把片子摆出来。也可以让它收藏、打标签、改影片信息，改之前会先让你确认。'}
                                </p>
                                {!about && <div className="asst-examples">
                                    {EXAMPLES.map((example) => (
                                        <button key={example} type="button" onClick={() => submit(example)}>
                                            {example}
                                        </button>
                                    ))}
                                </div>}
                            </div>
                        )}

                        {items.map((item, index) => {
                            if (item.role === 'user') {
                                const quoted = item.media?.[0];
                                if (!quoted) return <div key={index} className="asst-msg user">{item.text}</div>;
                                return (
                                    <div key={index} className="asst-user-turn">
                                        <QuotedMedia media={quoted} onOpenMedia={onOpenMedia} />
                                        <div className="asst-msg user">{item.text}</div>
                                    </div>
                                );
                            }
                            if (item.role === 'error') {
                                return <div key={index} className="asst-msg error">{item.text}</div>;
                            }
                            return (
                                <div key={index} className="asst-msg reply">
                                    {item.steps && item.steps.length > 0 && <StepSummary steps={item.steps} />}
                                    {item.changes?.map((change) => (
                                        <ProposalCard key={change.id} proposal={change} onOpenMedia={onOpenMedia} onStatus={onStatus} />
                                    ))}
                                    {item.text && <MiniMarkdown className="asst-md" text={item.text} />}
                                    {item.media && item.media.length > 0 && (
                                        <MediaSection media={item.media} onOpenMedia={onOpenMedia} onStatus={onStatus} />
                                    )}
                                </div>
                            );
                        })}

                        {pending && (
                            <>
                                {pendingAbout ? (
                                    <div className="asst-user-turn">
                                        <QuotedMedia media={pendingAbout} onOpenMedia={onOpenMedia} />
                                        <div className="asst-msg user">{pendingText}</div>
                                    </div>
                                ) : (
                                    <div className="asst-msg user">{pendingText}</div>
                                )}
                                <div className="asst-msg reply">
                                    <StepTimeline steps={liveSteps} live={!proposal} />
                                    {proposal && (
                                        <ProposalCard
                                            proposal={proposal}
                                            live
                                            deciding={deciding}
                                            onOpenMedia={onOpenMedia}
                                            onStatus={onStatus}
                                        />
                                    )}
                                </div>
                            </>
                        )}

                        {notice && <div className="asst-msg error">{notice}</div>}
                    </div>
                </div>

                <div className="asst-composer-wrap">
                    {about && !pending && !input.trim() && (
                        <div className="asst-about-prompts">
                            {ABOUT_PROMPTS.map((prompt) => (
                                <button key={prompt} type="button" onClick={() => submit(prompt)}>
                                    {prompt}
                                </button>
                            ))}
                        </div>
                    )}
                    <div className={`asst-composer ${about ? 'has-about' : ''}`.trim()}>
                        {about && (
                            <div className="asst-about">
                                <AboutPoster media={about} />
                                <span className="asst-about-text">
                                    <span className="asst-about-kicker"><Sparkles size={11} />问的是这部</span>
                                    <span className="asst-about-name">
                                        {about.code && <b>{about.code}</b>}
                                        <span>{about.title}</span>
                                    </span>
                                </span>
                                <button
                                    type="button"
                                    className="asst-icon-btn asst-about-remove"
                                    title="不带这部片"
                                    aria-label="不带这部片"
                                    onClick={() => setChatState({ about: null })}
                                >
                                    <X size={14} />
                                </button>
                            </div>
                        )}
                        <textarea
                            ref={inputRef}
                            value={input}
                            rows={1}
                            placeholder={about ? '问问关于这部片的事，比如：找几部类似的' : '比如：大胸年轻甜妹被男上司强攻的片子'}
                            onChange={(event) => setInput(event.target.value)}
                            onKeyDown={(event) => {
                                if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing) {
                                    event.preventDefault();
                                    submit(input);
                                }
                            }}
                        />
                        <div className="asst-composer-bar">
                            <PermissionPicker
                                value={settings?.permission || 'edit'}
                                disabled={pending || !settings}
                                onPick={(permission) => void saveSettingsPatch({ permission })}
                            />
                            <button
                                type="button"
                                className="asst-icon-btn"
                                title="接口预设"
                                onClick={() => setSettingsOpen(true)}
                            >
                                <Settings2 size={15} />
                            </button>
                            {/* 和常见的对话框一样：权限在左，模型和推理强度靠右挨着发送键 */}
                            <div className="asst-composer-right">
                            <ModelPicker
                                settings={settings}
                                disabled={pending}
                                onPick={(presetID, model) => void saveSettingsPatch({ active_preset: presetID, active_model: model })}
                                onManage={() => setSettingsOpen(true)}
                            />
                            <EffortPicker
                                value={settings?.effort || ''}
                                disabled={pending || !settings}
                                onPick={(effort) => void saveSettingsPatch({ effort })}
                            />
                            {pending ? (
                                <button type="button" className="asst-send stop" title="停止" onClick={() => void AssistantStop()}>
                                    <Square size={12} fill="currentColor" />
                                </button>
                            ) : (
                                <button
                                    type="button"
                                    className="asst-send"
                                    title="发送"
                                    disabled={!input.trim()}
                                    onClick={() => submit(input)}
                                >
                                    <SendHorizontal size={15} />
                                </button>
                            )}
                            </div>
                        </div>
                    </div>
                </div>
            </section>

            {settingsOpen && settings && (
                <AssistantSettingsModal
                    settings={settings}
                    onClose={() => setSettingsOpen(false)}
                    onSaved={setSettings}
                />
            )}
        </div>
    );
};

export default AssistantPage;
