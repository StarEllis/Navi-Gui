import React, { useEffect, useRef, useState } from 'react';
import { CalendarDays, Lock, UserRound, X } from 'lucide-react';
import type { NFOEditorDraft } from '../types/wails';

interface NFOEditModalProps {
    data: NFOEditorDraft | null;
    loading: boolean;
    saving: boolean;
    onClose: () => void;
    onSave: (draft: NFOEditorDraft) => Promise<void>;
}

interface NFOFieldProps {
    label: string;
    className?: string;
    children: React.ReactNode;
}

type TokenField = 'genres' | 'actors';
type TokenDrafts = Record<TokenField, string>;
type TokenEditState = {
    field: TokenField;
    index: number;
    value: string;
} | null;

const emptyForm: NFOEditorDraft = {
    nfo_path: '',
    title: '',
    code: '',
    release_date: '',
    director: '',
    series: '',
    publisher: '',
    maker: '',
    genres: '',
    actors: '',
    plot: '',
    runtime: '',
    file_size: '',
    resolution: '',
    video_codec: '',
    rating: '',
    source_fingerprint: '',
    updated_fields: [],
};

const editableFields = [
    'title',
    'code',
    'release_date',
    'series',
    'publisher',
    'maker',
    'director',
    'actors',
    'genres',
    'plot',
    'runtime',
    'rating',
] as const;

const fieldLabels: Record<(typeof editableFields)[number], string> = {
    title: '片名',
    code: '编号',
    release_date: '日期',
    series: '系列',
    publisher: '发行',
    maker: '制作',
    director: '导演',
    actors: '演员',
    genres: '类别',
    plot: '简介',
    runtime: '时长',
    rating: '评分',
};

const emptyTokenDrafts: TokenDrafts = {
    genres: '',
    actors: '',
};

const tokenSeparatorPattern = /[,，、/\n\r;|]+/;

const splitTokens = (raw: string) => raw
    .split(tokenSeparatorPattern)
    .map((token) => token.trim())
    .filter(Boolean);

const joinTokens = (tokens: string[]) => tokens.join(' / ');

const mergeTokens = (currentValue: string, draftValue: string) => {
    const merged = splitTokens(currentValue);
    const seen = new Set(merged.map((token) => token.toLowerCase()));

    splitTokens(draftValue).forEach((token) => {
        const key = token.toLowerCase();
        if (!seen.has(key)) {
            seen.add(key);
            merged.push(token);
        }
    });

    return joinTokens(merged);
};

const removeTokenAtIndex = (currentValue: string, index: number) => (
    joinTokens(splitTokens(currentValue).filter((_, tokenIndex) => tokenIndex !== index))
);

const replaceTokenAtIndex = (currentValue: string, index: number, nextValue: string) => {
    const value = nextValue.trim();
    if (!value) {
        return currentValue;
    }

    return joinTokens(splitTokens(currentValue).map((token, tokenIndex) => (
        tokenIndex === index ? value : token
    )));
};

const getTokenEditWidth = (value: string) => Array.from(value).reduce((width, character) => (
    width + (/[ᄀ-ᅟ⺀-꓏가-힣豈-﫿︐-﹯！-｠￠-￦]/u.test(character) ? 2 : 1)
), 0);

const compactPath = (path: string) => {
    if (!path) {
        return '未找到 NFO 文件';
    }

    const segments = path.split(/[\\/]+/).filter(Boolean);
    return segments.length > 2 ? `…\\${segments.slice(-2).join('\\')}` : path;
};

const NFOField: React.FC<NFOFieldProps> = ({ label, className = '', children }) => (
    <div className={`nfo-edit-row ${className}`.trim()}>
        <div className="nfo-edit-label">{label}</div>
        <div className="nfo-edit-value">{children}</div>
    </div>
);

const NFOEditModal: React.FC<NFOEditModalProps> = ({
    data,
    loading,
    saving,
    onClose,
    onSave,
}) => {
    const [form, setForm] = useState<NFOEditorDraft>(emptyForm);
    const [tokenDrafts, setTokenDrafts] = useState<TokenDrafts>(emptyTokenDrafts);
    const [tokenEdit, setTokenEdit] = useState<TokenEditState>(null);
    const tokenInputRefs = useRef<Record<TokenField, HTMLInputElement | null>>({
        genres: null,
        actors: null,
    });

    useEffect(() => {
        setForm({
            ...emptyForm,
            ...(data || {}),
        });
        setTokenDrafts(emptyTokenDrafts);
        setTokenEdit(null);
    }, [data]);

    useEffect(() => {
        const handleEscape = (event: KeyboardEvent) => {
            if (event.key === 'Escape' && !saving) {
                onClose();
            }
        };

        document.addEventListener('keydown', handleEscape);
        return () => document.removeEventListener('keydown', handleEscape);
    }, [onClose, saving]);

    const updateField = <K extends keyof NFOEditorDraft>(key: K, value: NFOEditorDraft[K]) => {
        setForm((prev) => ({ ...prev, [key]: value }));
    };

    const updateTokenDraft = (field: TokenField, value: string) => {
        setTokenDrafts((prev) => ({ ...prev, [field]: value }));
    };

    const commitTokenDraft = (field: TokenField, rawValue?: string) => {
        const draftValue = typeof rawValue === 'string' ? rawValue : tokenDrafts[field];
        if (!draftValue.trim()) {
            return false;
        }

        setForm((prev) => ({
            ...prev,
            [field]: mergeTokens(prev[field], draftValue),
        }));
        updateTokenDraft(field, '');
        return true;
    };

    const handleTokenKeyDown = (field: TokenField, event: React.KeyboardEvent<HTMLInputElement>) => {
        if (event.key === 'Enter') {
            event.preventDefault();
            commitTokenDraft(field, event.currentTarget.value);
            return;
        }

        if (event.key === 'Backspace' && event.currentTarget.value.trim() === '') {
            const tokens = splitTokens(form[field]);
            if (tokens.length > 0) {
                event.preventDefault();
                updateField(field, joinTokens(tokens.slice(0, -1)));
            }
        }
    };

    const commitTokenEdit = () => {
        if (!tokenEdit) {
            return;
        }

        const edit = tokenEdit;
        setForm((prev) => ({
            ...prev,
            [edit.field]: replaceTokenAtIndex(prev[edit.field], edit.index, edit.value),
        }));
        setTokenEdit(null);
    };

    const handleTokenRemove = (field: TokenField, index: number) => {
        setTokenEdit(null);
        setForm((prev) => ({
            ...prev,
            [field]: removeTokenAtIndex(prev[field], index),
        }));
    };

    const handleTokenEditKeyDown = (event: React.KeyboardEvent<HTMLInputElement>) => {
        if (event.key === 'Enter') {
            event.preventDefault();
            commitTokenEdit();
            return;
        }

        if (event.key === 'Escape') {
            event.preventDefault();
            event.stopPropagation();
            event.nativeEvent.stopImmediatePropagation();
            setTokenEdit(null);
        }
    };

    const buildSaveDraft = () => {
        const draft = {
            ...form,
            genres: mergeTokens(form.genres, tokenDrafts.genres),
            actors: mergeTokens(form.actors, tokenDrafts.actors),
        };

        return {
            ...draft,
            updated_fields: editableFields.filter((field) => draft[field] !== (data?.[field] ?? '')),
        };
    };

    const renderChips = (field: TokenField) => {
        const tokens = splitTokens(form[field]);
        const isActor = field === 'actors';

        return (
            <div className={`nfo-edit-chip-list ${isActor ? 'is-actors' : 'is-genres'}`}>
                {tokens.map((token, index) => {
                    const isEditingThisToken = tokenEdit?.field === field && tokenEdit.index === index;

                    return (
                        <span
                            key={`${token}-${index}`}
                            className={`nfo-edit-chip${isEditingThisToken ? ' is-editing' : ''}`}
                        >
                        {isActor && <UserRound size={12} aria-hidden="true" />}
                        {isEditingThisToken ? (
                            <input
                                className="nfo-edit-chip-input"
                                value={tokenEdit.value}
                                style={{ width: `${Math.min(32, Math.max(4, getTokenEditWidth(tokenEdit.value) + 1))}ch` }}
                                onChange={(event) => setTokenEdit({ field, index, value: event.target.value })}
                                onKeyDown={handleTokenEditKeyDown}
                                onBlur={commitTokenEdit}
                                autoFocus
                                aria-label={`编辑 ${token}`}
                            />
                        ) : (
                            <button
                                type="button"
                                className="nfo-edit-chip-label"
                                title={`点击编辑 ${token}`}
                                onClick={() => setTokenEdit({ field, index, value: token })}
                            >
                                {token}
                            </button>
                        )}
                        <button
                            type="button"
                            className="nfo-edit-chip-remove"
                            title={`删除 ${token}`}
                            aria-label={`删除 ${token}`}
                            onClick={() => handleTokenRemove(field, index)}
                        >
                            <X className="nfo-edit-chip-close" size={11} aria-hidden="true" />
                        </button>
                    </span>
                    );
                })}

                <button
                    type="button"
                    className="nfo-edit-chip-add"
                    onClick={() => tokenInputRefs.current[field]?.focus()}
                >
                    + 添加
                </button>
                <input
                    ref={(element) => { tokenInputRefs.current[field] = element; }}
                    className="nfo-edit-token-input"
                    value={tokenDrafts[field]}
                    onChange={(event) => updateTokenDraft(field, event.target.value)}
                    onKeyDown={(event) => handleTokenKeyDown(field, event)}
                    onBlur={(event) => commitTokenDraft(field, event.currentTarget.value)}
                    autoComplete="off"
                    spellCheck={false}
                    aria-label={isActor ? '添加演员' : '添加类别'}
                />
                {!isActor && <span className="nfo-edit-token-count">共 {tokens.length}</span>}
            </div>
        );
    };

    const changedFields = buildSaveDraft().updated_fields || [];
    const changedSummary = changedFields.map((field) => fieldLabels[field as keyof typeof fieldLabels]).join('、');

    return (
        <div className="modal-overlay nfo-edit-overlay" onClick={() => !saving && onClose()}>
            <div className="nfo-edit-modal" onClick={(event) => event.stopPropagation()}>
                <header className="nfo-edit-header">
                    <div className="nfo-edit-header-copy">
                        <div className="nfo-edit-title">编辑 NFO</div>
                        <div className="nfo-edit-path" title={form.nfo_path}>{compactPath(form.nfo_path)}</div>
                    </div>
                    <button
                        type="button"
                        className="nfo-edit-close"
                        onClick={onClose}
                        disabled={saving}
                        aria-label="关闭"
                    >
                        <X size={16} />
                    </button>
                </header>

                <div className="nfo-edit-body">
                    {loading ? (
                        <div className="nfo-edit-loading">
                            <div className="nfo-edit-loading-title">正在读取 NFO…</div>
                            <div className="nfo-edit-loading-subtitle">请稍候，正在加载编辑内容。</div>
                        </div>
                    ) : (
                        <>
                            <NFOField label="片名">
                                <textarea
                                    className="nfo-edit-input nfo-edit-title-input"
                                    rows={1}
                                    value={form.title}
                                    placeholder="点击填写"
                                    onChange={(event) => updateField('title', event.target.value)}
                                    spellCheck={false}
                                />
                            </NFOField>

                            <NFOField label="编号">
                                <input
                                    className="nfo-edit-input nfo-edit-mono nfo-edit-code-input"
                                    style={{ width: `${Math.min(32, Math.max(4, getTokenEditWidth(form.code) + 1))}ch` }}
                                    value={form.code}
                                    placeholder="点击填写"
                                    onChange={(event) => updateField('code', event.target.value)}
                                    autoComplete="off"
                                    spellCheck={false}
                                />
                            </NFOField>

                            <NFOField label="日期">
                                <div className="nfo-edit-date">
                                    <input
                                        className="nfo-edit-input nfo-edit-mono"
                                        value={form.release_date}
                                        placeholder="点击填写"
                                        onChange={(event) => updateField('release_date', event.target.value)}
                                        autoComplete="off"
                                        spellCheck={false}
                                    />
                                    <CalendarDays size={13} aria-hidden="true" />
                                </div>
                            </NFOField>

                            <NFOField label="系列">
                                <input className="nfo-edit-input" value={form.series} placeholder="点击填写" onChange={(event) => updateField('series', event.target.value)} />
                            </NFOField>

                            <NFOField label="发行">
                                <div className="nfo-edit-publisher-line">
                                    <input className="nfo-edit-input" value={form.publisher} placeholder="点击填写" onChange={(event) => updateField('publisher', event.target.value)} />
                                    <span className="nfo-edit-inline-label">制作</span>
                                    <input className="nfo-edit-input nfo-edit-maker-input" value={form.maker} placeholder="点击填写" onChange={(event) => updateField('maker', event.target.value)} />
                                </div>
                            </NFOField>

                            <NFOField label="导演">
                                <input className="nfo-edit-input" value={form.director} placeholder="点击填写" onChange={(event) => updateField('director', event.target.value)} />
                            </NFOField>

                            <NFOField label="演员" className="nfo-edit-row--tokens">
                                {renderChips('actors')}
                            </NFOField>

                            <NFOField label="类别" className="nfo-edit-row--tokens">
                                {renderChips('genres')}
                            </NFOField>

                            <NFOField label="简介" className="nfo-edit-row--plot">
                                <textarea
                                    className="nfo-edit-textarea"
                                    value={form.plot}
                                    placeholder="点击填写"
                                    onChange={(event) => updateField('plot', event.target.value)}
                                    spellCheck={false}
                                />
                            </NFOField>

                            <NFOField label="时长">
                                <div className="nfo-edit-metrics">
                                    <input className="nfo-edit-input nfo-edit-mono nfo-edit-runtime-input" value={form.runtime} placeholder="点击填写" onChange={(event) => updateField('runtime', event.target.value)} />
                                    <span className="nfo-edit-unit">分钟</span>
                                    <span className="nfo-edit-metric-label">评分</span>
                                    <input className="nfo-edit-input nfo-edit-mono nfo-edit-rating-input" value={form.rating} placeholder="点击填写" onChange={(event) => updateField('rating', event.target.value)} />
                                </div>
                            </NFOField>

                            <NFOField label="文件">
                                <div className="nfo-edit-file-meta">
                                    <span>{[form.file_size, form.resolution, form.video_codec].filter(Boolean).join(' · ') || '暂无文件信息'}</span>
                                    <Lock size={11} aria-hidden="true" />
                                    <span className="nfo-edit-file-note">读取自文件，不可改</span>
                                </div>
                            </NFOField>
                        </>
                    )}
                </div>

                <footer className="nfo-edit-footer">
                    <div className="nfo-edit-change-summary" aria-live="polite">
                        {changedFields.length > 0 && (
                            <>
                                <span className="nfo-edit-change-dot" />
                                <span>{changedSummary}已修改</span>
                            </>
                        )}
                    </div>
                    <div className="nfo-edit-footer-actions">
                        <button type="button" className="nfo-edit-action ghost" onClick={onClose} disabled={saving}>取消</button>
                        <button
                            type="button"
                            className="nfo-edit-action primary"
                            onClick={() => onSave(buildSaveDraft())}
                            disabled={loading || saving}
                        >
                            {saving ? '保存中…' : '保存'}
                        </button>
                    </div>
                </footer>
            </div>
        </div>
    );
};

export default NFOEditModal;
