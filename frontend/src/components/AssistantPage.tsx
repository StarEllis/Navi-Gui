import React, { useEffect, useRef, useState, useSyncExternalStore } from 'react';
import {
    Brain,
    Check,
    ChevronDown,
    ChevronRight,
    Database,
    Images,
    LoaderCircle,
    MessageSquare,
    PanelLeftClose,
    PanelLeftOpen,
    Plus,
    SendHorizontal,
    Settings2,
    Sparkles,
    Square,
    Trash2,
} from 'lucide-react';
import {
    AssistantCurrentConversation,
    AssistantDeleteConversation,
    AssistantListConversations,
    AssistantNewConversation,
    AssistantOpenConversation,
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
import type { StatusKind } from '../types/status';
import '../assistant.css';

type ChatState = {
    conversation: assistant.Conversation | null;
    list: assistant.ConversationSummary[];
    pendingText: string; // 非空表示正在等回答
    notice: string; // 没进会话的错误，比如上一句还没完
};

const EXAMPLES = [
    'S1 的 4K 片子有多少部？',
    '哪个片商的片子我收得最多？',
    '还没看过的里面，评分最高的 10 部',
    '女上司被下属反攻的片子',
];

// 会话状态放在模块里而不是组件里：切到别的页面再回来还在，
// 请求没回来就切走，结果也照样落进来。
let chatState: ChatState = { conversation: null, list: [], pendingText: '', notice: '' };
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

const sendMessage = async (text: string) => {
    if (chatState.pendingText) return;
    setChatState({ pendingText: text, notice: '' });
    try {
        const conversation = await AssistantSend(text);
        setChatState({ conversation });
        void refreshList();
    } catch (error) {
        setChatState({ notice: formatError(error) });
    } finally {
        setChatState({ pendingText: '' });
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
    return tool === 'show_media' ? <Images size={13} /> : <Database size={13} />;
};
const stepLabel = (tool: string) => {
    if (tool === 'thinking') return '思考';
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
    const parts = ['思考过程', `查询 ${queries} 次`];
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
    const { conversation, list, pendingText, notice } = useSyncExternalStore(subscribeChat, () => chatState);
    const pending = pendingText !== '';
    const [input, setInput] = useState('');
    const [liveSteps, setLiveSteps] = useState<assistant.Step[]>([]);
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
        if (!chatState.conversation && !chatState.pendingText) {
            AssistantCurrentConversation()
                .then((current) => {
                    if (current) setChatState({ conversation: current });
                })
                .catch(() => undefined);
        }
    }, [onStatus]);

    useEffect(() => EventsOn('assistant-step', (step: assistant.Step) => {
        setLiveSteps((steps) => [...steps, step]);
    }), []);

    useEffect(() => {
        if (!pending) setLiveSteps([]);
    }, [pending]);

    useEffect(() => {
        const node = scrollRef.current;
        if (node) node.scrollTop = node.scrollHeight;
    }, [conversation?.id, conversation?.items.length, pending, liveSteps.length]);

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
                {!sidebarCollapsed && <div className="asst-sidebar-foot">只读版 · 只能查，不能改</div>}
            </aside>

            <section className="asst-main">
                <div className="asst-scroll" ref={scrollRef}>
                    <div className="asst-thread">
                        {showEmpty && (
                            <div className="asst-empty">
                                <div className="asst-empty-icon"><Sparkles size={20} /></div>
                                <h2>用一句话找片</h2>
                                <p>描述条件、剧情或者想看的统计，助手会去库里查，再把片子摆出来。</p>
                                <div className="asst-examples">
                                    {EXAMPLES.map((example) => (
                                        <button key={example} type="button" onClick={() => submit(example)}>
                                            {example}
                                        </button>
                                    ))}
                                </div>
                            </div>
                        )}

                        {items.map((item, index) => {
                            if (item.role === 'user') {
                                return <div key={index} className="asst-msg user">{item.text}</div>;
                            }
                            if (item.role === 'error') {
                                return <div key={index} className="asst-msg error">{item.text}</div>;
                            }
                            return (
                                <div key={index} className="asst-msg reply">
                                    {item.steps && item.steps.length > 0 && <StepSummary steps={item.steps} />}
                                    {item.text && <MiniMarkdown className="asst-md" text={item.text} />}
                                    {item.media && item.media.length > 0 && (
                                        <MediaSection media={item.media} onOpenMedia={onOpenMedia} onStatus={onStatus} />
                                    )}
                                </div>
                            );
                        })}

                        {pending && (
                            <>
                                <div className="asst-msg user">{pendingText}</div>
                                <div className="asst-msg reply">
                                    <StepTimeline steps={liveSteps} live />
                                </div>
                            </>
                        )}

                        {notice && <div className="asst-msg error">{notice}</div>}
                    </div>
                </div>

                <div className="asst-composer-wrap">
                    <div className="asst-composer">
                        <textarea
                            value={input}
                            rows={1}
                            placeholder="比如：大胸年轻甜妹被男上司强攻的片子"
                            onChange={(event) => setInput(event.target.value)}
                            onKeyDown={(event) => {
                                if (event.key === 'Enter' && !event.shiftKey && !event.nativeEvent.isComposing) {
                                    event.preventDefault();
                                    submit(input);
                                }
                            }}
                        />
                        <div className="asst-composer-bar">
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
                            <button
                                type="button"
                                className="asst-icon-btn"
                                title="接口预设"
                                onClick={() => setSettingsOpen(true)}
                            >
                                <Settings2 size={15} />
                            </button>
                            <span className="asst-composer-hint">Enter 发送 · Shift+Enter 换行</span>
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
