import React, { useEffect, useState } from 'react';
import { CalendarDays, Camera, X } from 'lucide-react';
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
	'director',
	'series',
	'publisher',
	'maker',
	'genres',
	'actors',
	'plot',
	'runtime',
	'rating',
] as const;

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
        if (seen.has(key)) {
            return;
        }

        seen.add(key);
        merged.push(token);
    });

    return joinTokens(merged);
};

const removeTokenAtIndex = (currentValue: string, index: number) => {
    const tokens = splitTokens(currentValue);
    return joinTokens(tokens.filter((_, tokenIndex) => tokenIndex !== index));
};

const NFOField: React.FC<NFOFieldProps> = ({ label, className = '', children }) => (
    <div className={`nfo-edit-field ${className}`.trim()}>
        <label className="nfo-edit-label">{label}</label>
        {children}
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

    useEffect(() => {
        setForm({
            ...emptyForm,
            ...(data || {}),
        });
        setTokenDrafts(emptyTokenDrafts);
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
        setForm((prev) => ({
            ...prev,
            [key]: value,
        }));
    };

    const updateTokenDraft = (field: TokenField, value: string) => {
        setTokenDrafts((prev) => ({
            ...prev,
            [field]: value,
        }));
    };

    const commitTokenDraft = (field: TokenField, rawValue?: string) => {
        const draftValue = typeof rawValue === 'string' ? rawValue : tokenDrafts[field];
        const trimmedDraft = draftValue.trim();

        if (!trimmedDraft) {
            return false;
        }

        setForm((prev) => ({
            ...prev,
            [field]: mergeTokens(prev[field], trimmedDraft),
        }));
        updateTokenDraft(field, '');
        return true;
    };

    const handleTokenRemove = (field: TokenField, index: number) => {
        setForm((prev) => ({
            ...prev,
            [field]: removeTokenAtIndex(prev[field], index),
        }));
    };

    const handleTokenKeyDown = (
        field: TokenField,
        event: React.KeyboardEvent<HTMLInputElement>,
    ) => {
        if (event.key === 'Enter') {
            event.preventDefault();
            commitTokenDraft(field, event.currentTarget.value);
            return;
        }

        if (event.key === 'Backspace' && event.currentTarget.value.trim() === '') {
            const currentTokens = splitTokens(form[field]);
            if (currentTokens.length === 0) {
                return;
            }

            event.preventDefault();
            setForm((prev) => ({
                ...prev,
                [field]: joinTokens(currentTokens.slice(0, -1)),
            }));
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

    const renderChips = (field: TokenField, rawValue: string) => {
        const tokens = splitTokens(rawValue);

        if (tokens.length === 0) {
            return null;
        }

        return tokens.map((token, index) => (
            <button
                key={`${token}-${index}`}
                type="button"
                className="nfo-edit-chip"
                title={`点击移除 ${token}`}
                onClick={() => handleTokenRemove(field, index)}
            >
                <span className="nfo-edit-chip-text">{token}</span>
                <span className="nfo-edit-chip-close" aria-hidden="true">×</span>
            </button>
        ));
    };

    return (
        <div className="modal-overlay nfo-edit-overlay" onClick={() => !saving && onClose()}>
            <div className="nfo-edit-modal" onClick={(event) => event.stopPropagation()}>
                <div className="nfo-edit-header">
                    <div className="nfo-edit-brand">
                        <div className="nfo-edit-brand-icon" aria-hidden="true">
                            <Camera size={18} />
                        </div>
                        <div className="nfo-edit-brand-copy">
                            <div className="nfo-edit-title">
                                <span className="nfo-edit-title-brand">Navi</span>
                                <span className="nfo-edit-title-text">媒体 NFO编辑器</span>
                            </div>
                            <div className="nfo-edit-path" title={form.nfo_path}>
                                {form.nfo_path || '未找到 NFO 文件'}
                            </div>
                        </div>
                    </div>

                    <button
                        type="button"
                        className="nfo-edit-close"
                        onClick={onClose}
                        disabled={saving}
                        aria-label="关闭"
                    >
                        <X size={18} />
                    </button>
                </div>

                <div className="nfo-edit-body">
                    {loading ? (
                        <div className="nfo-edit-loading">
                            <div className="nfo-edit-loading-card">
                                <div className="nfo-edit-loading-title">正在读取 NFO…</div>
                                <div className="nfo-edit-loading-subtitle">请稍候，正在加载编辑内容。</div>
                            </div>
                        </div>
                    ) : (
                        <>
                            <NFOField label="片名" className="nfo-edit-field--featured">
                                <div className="nfo-edit-input-shell nfo-edit-input-shell--featured">
                                    <input
                                        className="nfo-edit-input"
                                        value={form.title}
                                        onChange={(event) => updateField('title', event.target.value)}
                                        autoComplete="off"
                                        spellCheck={false}
                                    />
                                </div>
                            </NFOField>

                            <div className="nfo-edit-grid three">
                                <NFOField label="编号">
                                    <div className="nfo-edit-input-shell">
                                        <input
                                            className="nfo-edit-input"
                                            value={form.code}
                                            onChange={(event) => updateField('code', event.target.value)}
                                            autoComplete="off"
                                            spellCheck={false}
                                        />
                                    </div>
                                </NFOField>
                                <NFOField label="日期">
                                    <div className="nfo-edit-input-shell nfo-edit-input-shell--date">
                                        <input
                                            className="nfo-edit-input"
                                            value={form.release_date}
                                            onChange={(event) => updateField('release_date', event.target.value)}
                                            placeholder="YYYY-MM-DD"
                                            autoComplete="off"
                                            spellCheck={false}
                                        />
                                        <CalendarDays size={14} className="nfo-edit-input-icon" aria-hidden="true" />
                                    </div>
                                </NFOField>
                                <NFOField label="导演">
                                    <div className="nfo-edit-input-shell">
                                        <input
                                            className="nfo-edit-input"
                                            value={form.director}
                                            onChange={(event) => updateField('director', event.target.value)}
                                            autoComplete="off"
                                            spellCheck={false}
                                        />
                                    </div>
                                </NFOField>
                            </div>

                            <div className="nfo-edit-grid three">
                                <NFOField label="系列">
                                    <div className="nfo-edit-input-shell">
                                        <input
                                            className="nfo-edit-input"
                                            value={form.series}
                                            onChange={(event) => updateField('series', event.target.value)}
                                            autoComplete="off"
                                            spellCheck={false}
                                        />
                                    </div>
                                </NFOField>
                                <NFOField label="发行">
                                    <div className="nfo-edit-input-shell">
                                        <input
                                            className="nfo-edit-input"
                                            value={form.publisher}
                                            onChange={(event) => updateField('publisher', event.target.value)}
                                            autoComplete="off"
                                            spellCheck={false}
                                        />
                                    </div>
                                </NFOField>
                                <NFOField label="制作">
                                    <div className="nfo-edit-input-shell">
                                        <input
                                            className="nfo-edit-input"
                                            value={form.maker}
                                            onChange={(event) => updateField('maker', event.target.value)}
                                            autoComplete="off"
                                            spellCheck={false}
                                        />
                                    </div>
                                </NFOField>
                            </div>

                            <NFOField label="类别">
                                <div className="nfo-edit-token-shell">
                                    <div className="nfo-edit-chip-list">
                                        {renderChips('genres', form.genres)}
                                        <input
                                            className="nfo-edit-token-input"
                                            value={tokenDrafts.genres}
                                            onChange={(event) => updateTokenDraft('genres', event.target.value)}
                                            onKeyDown={(event) => handleTokenKeyDown('genres', event)}
                                            onBlur={(event) => commitTokenDraft('genres', event.currentTarget.value)}
                                            autoComplete="off"
                                            spellCheck={false}
                                            aria-label="添加类别标签"
                                        />
                                    </div>
                                </div>
                            </NFOField>

                            <NFOField label="演员">
                                <div className="nfo-edit-token-shell">
                                    <div className="nfo-edit-chip-list">
                                        {renderChips('actors', form.actors)}
                                        <input
                                            className="nfo-edit-token-input"
                                            value={tokenDrafts.actors}
                                            onChange={(event) => updateTokenDraft('actors', event.target.value)}
                                            onKeyDown={(event) => handleTokenKeyDown('actors', event)}
                                            onBlur={(event) => commitTokenDraft('actors', event.currentTarget.value)}
                                            autoComplete="off"
                                            spellCheck={false}
                                            aria-label="添加演员标签"
                                        />
                                    </div>
                                </div>
                            </NFOField>

                            <NFOField label="简介">
                                <div className="nfo-edit-textarea-shell">
                                    <textarea
                                        className="nfo-edit-textarea"
                                        value={form.plot}
                                        onChange={(event) => updateField('plot', event.target.value)}
                                        autoComplete="off"
                                        spellCheck={false}
                                    />
                                </div>
                            </NFOField>

                            <div className="nfo-edit-grid metrics">
                                <NFOField label="时长">
                                    <div className="nfo-edit-input-shell">
                                        <input
                                            className="nfo-edit-input compact"
                                            value={form.runtime}
                                            onChange={(event) => updateField('runtime', event.target.value)}
                                            autoComplete="off"
                                            spellCheck={false}
                                        />
                                    </div>
                                </NFOField>
                                <NFOField label="大小">
                                    <div className="nfo-edit-input-shell">
                                        <input
                                            className="nfo-edit-input compact readonly"
                                            value={form.file_size}
                                            readOnly
                                            tabIndex={-1}
                                        />
                                    </div>
                                </NFOField>
                                <NFOField label="分辨率">
                                    <div className="nfo-edit-input-shell">
                                        <input
                                            className="nfo-edit-input compact readonly"
                                            value={form.resolution}
                                            readOnly
                                            tabIndex={-1}
                                        />
                                    </div>
                                </NFOField>
                                <NFOField label="编码">
                                    <div className="nfo-edit-input-shell">
                                        <input
                                            className="nfo-edit-input compact readonly"
                                            value={form.video_codec}
                                            readOnly
                                            tabIndex={-1}
                                        />
                                    </div>
                                </NFOField>
                                <NFOField label="评分">
                                    <div className="nfo-edit-input-shell">
                                        <input
                                            className="nfo-edit-input compact"
                                            value={form.rating}
                                            onChange={(event) => updateField('rating', event.target.value)}
                                            autoComplete="off"
                                            spellCheck={false}
                                        />
                                    </div>
                                </NFOField>
                            </div>
                        </>
                    )}
                </div>

                <div className="nfo-edit-footer">
                    <button type="button" className="nfo-edit-action ghost" onClick={onClose} disabled={saving}>
                        取消
                    </button>
                    <button
                        type="button"
                        className="nfo-edit-action primary"
                        onClick={() => onSave(buildSaveDraft())}
                        disabled={loading || saving}
                    >
                        {saving ? '保存中…' : '保存'}
                    </button>
                </div>
            </div>
        </div>
    );
};

export default NFOEditModal;
