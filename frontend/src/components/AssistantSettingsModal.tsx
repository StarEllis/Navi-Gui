import React, { useEffect, useRef, useState } from 'react';
import { Check, Eye, EyeOff, Plus, RefreshCw, Trash2, X } from 'lucide-react';
import { ListAssistantModels, SaveAssistantSettings } from '../../wailsjs/go/main/App';
import { assistant } from '../../wailsjs/go/models';

const API_TYPES = [
    { value: 'openai', label: 'OpenAI 兼容', hint: 'DeepSeek、OpenRouter、中转站等' },
    { value: 'anthropic', label: 'Anthropic', hint: 'Claude 官方接口或兼容中转' },
];

const formatError = (error: unknown) => {
    if (error instanceof Error && error.message) return error.message;
    if (typeof error === 'string' && error) return error;
    return '未知错误';
};

const newPreset = (): assistant.Preset => assistant.Preset.createFrom({
    id: `p-${Date.now().toString(36)}`,
    name: '新预设',
    api_type: 'openai',
    base_url: '',
    api_key: '',
    models: [],
});

type Notice = { kind: 'ok' | 'error'; text: string } | null;

interface Props {
    settings: assistant.Settings;
    onClose: () => void;
    onSaved: (settings: assistant.Settings) => void;
}

const AssistantSettingsModal: React.FC<Props> = ({ settings, onClose, onSaved }) => {
    const [draft, setDraft] = useState<assistant.Preset[]>(() => settings.presets.map((p) => ({ ...p, models: [...(p.models || [])] })));
    const [selectedID, setSelectedID] = useState(() => settings.active_preset || settings.presets[0]?.id || '');
    const [showKey, setShowKey] = useState(false);
    const [newModel, setNewModel] = useState('');
    const [fetching, setFetching] = useState(false);
    const [saving, setSaving] = useState(false);
    const [confirmDelete, setConfirmDelete] = useState(false);
    const [notice, setNotice] = useState<Notice>(null);
    const [dirty, setDirty] = useState(false);
    const noticeTimer = useRef<number | null>(null);

    const selected = draft.find((p) => p.id === selectedID) || null;

    useEffect(() => () => {
        if (noticeTimer.current !== null) window.clearTimeout(noticeTimer.current);
    }, []);

    useEffect(() => {
        setConfirmDelete(false);
        setShowKey(false);
        setNewModel('');
    }, [selectedID]);

    // 提示只停一会儿：成功 1.5 秒，出错留久一点好看清
    const flash = (next: Notice) => {
        setNotice(next);
        if (noticeTimer.current !== null) window.clearTimeout(noticeTimer.current);
        noticeTimer.current = window.setTimeout(() => setNotice(null), next?.kind === 'error' ? 4000 : 1500);
    };

    const updateSelected = (patch: Partial<assistant.Preset>) => {
        setDraft((presets) => presets.map((p) => (p.id === selectedID ? { ...p, ...patch } as assistant.Preset : p)));
        setDirty(true);
    };

    const addPreset = () => {
        const preset = newPreset();
        setDraft((presets) => [...presets, preset]);
        setSelectedID(preset.id);
        setDirty(true);
    };

    const deletePreset = () => {
        const remaining = draft.filter((p) => p.id !== selectedID);
        setDraft(remaining);
        setSelectedID(remaining[0]?.id || '');
        setDirty(true);
    };

    const addModels = (models: string[]) => {
        if (!selected) return;
        const merged = [...selected.models];
        models.map((m) => m.trim()).filter(Boolean).forEach((m) => {
            if (!merged.includes(m)) merged.push(m);
        });
        updateSelected({ models: merged });
    };

    const fetchModels = async () => {
        if (!selected) return;
        setFetching(true);
        try {
            const models = await ListAssistantModels(assistant.Config.createFrom({
                api_type: selected.api_type,
                base_url: selected.base_url,
                api_key: selected.api_key,
                model: '',
            }));
            const fresh = models.filter((m) => !selected.models.includes(m)).length;
            addModels(models);
            flash({ kind: 'ok', text: fresh > 0 ? `新增 ${fresh} 个模型` : '没有新模型' });
        } catch (error) {
            flash({ kind: 'error', text: `拉取失败：${formatError(error)}` });
        } finally {
            setFetching(false);
        }
    };

    const save = async () => {
        if (draft.some((p) => !p.name.trim())) {
            flash({ kind: 'error', text: '预设名称不能为空' });
            return;
        }
        setSaving(true);
        try {
            // 当前在用的预设被删了，后端会退回第一个
            const saved = await SaveAssistantSettings(assistant.Settings.createFrom({
                presets: draft,
                active_preset: settings.active_preset,
                active_model: settings.active_model,
            }));
            onSaved(saved);
            setDirty(false);
            flash({ kind: 'ok', text: '已保存' });
        } catch (error) {
            flash({ kind: 'error', text: `保存失败：${formatError(error)}` });
        } finally {
            setSaving(false);
        }
    };

    const requestClose = () => {
        if (dirty && !window.confirm('有改动还没保存，确定关闭吗？')) return;
        onClose();
    };

    return (
        <div className="asst-modal-backdrop" onMouseDown={(event) => event.target === event.currentTarget && requestClose()}>
            <div className="asst-modal" role="dialog" aria-label="接口预设">
                <div className="asst-modal-head">
                    <h3>接口预设</h3>
                    <button type="button" className="asst-icon-btn" title="关闭" onClick={requestClose}>
                        <X size={16} />
                    </button>
                </div>

                <div className="asst-modal-body">
                    <div className="asst-preset-list">
                        {draft.map((preset) => (
                            <button
                                key={preset.id}
                                type="button"
                                className={`asst-preset-item ${preset.id === selectedID ? 'active' : ''}`.trim()}
                                onClick={() => setSelectedID(preset.id)}
                            >
                                <span className="asst-preset-name">{preset.name || '未命名'}</span>
                                <span className="asst-preset-meta">
                                    {preset.api_type === 'anthropic' ? 'Anthropic' : 'OpenAI 兼容'} · {preset.models.length} 个模型
                                </span>
                            </button>
                        ))}
                        <button type="button" className="asst-preset-add" onClick={addPreset}>
                            <Plus size={14} />
                            新建预设
                        </button>
                    </div>

                    {selected ? (
                        <div className="asst-preset-editor">
                            <label className="asst-field">
                                <span>名称</span>
                                <input
                                    className="asst-input"
                                    value={selected.name}
                                    placeholder="比如：家里中转站"
                                    onChange={(event) => updateSelected({ name: event.target.value })}
                                />
                            </label>

                            <div className="asst-field">
                                <span>接口类型</span>
                                <div className="asst-segmented">
                                    {API_TYPES.map((type) => (
                                        <button
                                            key={type.value}
                                            type="button"
                                            className={selected.api_type === type.value ? 'active' : ''}
                                            onClick={() => updateSelected({ api_type: type.value })}
                                        >
                                            <strong>{type.label}</strong>
                                            <small>{type.hint}</small>
                                        </button>
                                    ))}
                                </div>
                            </div>

                            <label className="asst-field">
                                <span>接口地址</span>
                                <input
                                    className="asst-input mono"
                                    value={selected.base_url}
                                    placeholder={selected.api_type === 'anthropic' ? '留空使用官方地址' : '留空使用 https://api.openai.com/v1'}
                                    onChange={(event) => updateSelected({ base_url: event.target.value })}
                                />
                            </label>

                            <label className="asst-field">
                                <span>API Key</span>
                                <div className="asst-input-row">
                                    <input
                                        className="asst-input mono"
                                        type={showKey ? 'text' : 'password'}
                                        value={selected.api_key}
                                        autoComplete="off"
                                        onChange={(event) => updateSelected({ api_key: event.target.value })}
                                    />
                                    <button
                                        type="button"
                                        className="asst-icon-btn"
                                        title={showKey ? '隐藏' : '显示'}
                                        onClick={() => setShowKey((show) => !show)}
                                    >
                                        {showKey ? <EyeOff size={15} /> : <Eye size={15} />}
                                    </button>
                                </div>
                            </label>

                            <div className="asst-field">
                                <div className="asst-field-head">
                                    <span>模型（{selected.models.length}）</span>
                                    <button
                                        type="button"
                                        className="asst-text-btn"
                                        disabled={fetching || !selected.api_key.trim()}
                                        title={selected.api_key.trim() ? '从接口拉取可用模型' : '先填 API Key'}
                                        onClick={() => void fetchModels()}
                                    >
                                        <RefreshCw size={13} className={fetching ? 'spin' : ''} />
                                        {fetching ? '拉取中…' : '拉取模型'}
                                    </button>
                                </div>
                                <div className="asst-chips">
                                    {selected.models.length === 0 && <span className="asst-empty-hint">还没有模型，拉取或者手动添加</span>}
                                    {selected.models.map((model) => (
                                        <span key={model} className="asst-chip">
                                            {model}
                                            <button
                                                type="button"
                                                title="移除"
                                                onClick={() => updateSelected({ models: selected.models.filter((m) => m !== model) })}
                                            >
                                                <X size={12} />
                                            </button>
                                        </span>
                                    ))}
                                </div>
                                <div className="asst-input-row">
                                    <input
                                        className="asst-input mono"
                                        value={newModel}
                                        placeholder="手动添加模型名，回车确认"
                                        onChange={(event) => setNewModel(event.target.value)}
                                        onKeyDown={(event) => {
                                            if (event.key === 'Enter' && !event.nativeEvent.isComposing) {
                                                event.preventDefault();
                                                addModels([newModel]);
                                                setNewModel('');
                                            }
                                        }}
                                    />
                                    <button
                                        type="button"
                                        className="asst-btn"
                                        disabled={!newModel.trim()}
                                        onClick={() => {
                                            addModels([newModel]);
                                            setNewModel('');
                                        }}
                                    >
                                        添加
                                    </button>
                                </div>
                            </div>

                            <div className="asst-danger-zone">
                                {confirmDelete ? (
                                    <>
                                        <span>删除「{selected.name || '未命名'}」？保存后生效。</span>
                                        <button type="button" className="asst-btn danger" onClick={deletePreset}>删除</button>
                                        <button type="button" className="asst-btn" onClick={() => setConfirmDelete(false)}>取消</button>
                                    </>
                                ) : (
                                    <button type="button" className="asst-text-btn danger" onClick={() => setConfirmDelete(true)}>
                                        <Trash2 size={13} />
                                        删除这个预设
                                    </button>
                                )}
                            </div>
                        </div>
                    ) : (
                        <div className="asst-preset-empty">
                            <p>还没有接口预设。</p>
                            <button type="button" className="asst-btn primary" onClick={addPreset}>
                                <Plus size={14} />
                                新建一个
                            </button>
                        </div>
                    )}
                </div>

                <div className="asst-modal-foot">
                    <span className="asst-foot-hint">Key 只保存在本机的 data/assistant.json 里。</span>
                    {notice && (
                        <span className={`asst-notice ${notice.kind}`}>
                            {notice.kind === 'ok' && <Check size={13} />}
                            {notice.text}
                        </span>
                    )}
                    <button type="button" className="asst-btn" onClick={requestClose}>关闭</button>
                    <button type="button" className="asst-btn primary" disabled={saving || !dirty} onClick={() => void save()}>
                        {saving ? '保存中…' : '保存'}
                    </button>
                </div>
            </div>
        </div>
    );
};

export default AssistantSettingsModal;
