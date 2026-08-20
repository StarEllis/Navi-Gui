import React, { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react';
import { FolderInput, Merge, MoreHorizontal, Pencil, Trash2, X } from 'lucide-react';
import {
    DeleteMyTag,
    MergeMyTags,
    MoveMyTag,
    RenameMyTag,
} from '../../wailsjs/go/main/App';
import {
    buildCategoryColorMap,
    categoryColor,
    getUserTagsSnapshot,
    invalidateUserTags,
    loadUserTags,
    subscribeUserTags,
    UNCATEGORIZED_DOT,
    UNCATEGORIZED_LABEL,
    type UserTag,
} from '../utils/userTags';
import type { StatusKind } from '../types/status';

interface TagsPageProps {
    onStatus?: (message: string, kind?: StatusKind) => void;
    // 标签改名/合并/删除后，海报墙和详情页上的标签也变了
    onTagsMutated?: () => void;
}

type PendingPrompt =
    | { kind: 'renameCategory'; category: string; tags: UserTag[] }
    | { kind: 'rename'; tag: UserTag }
    | { kind: 'move'; tag: UserTag }
    | { kind: 'merge'; tag: UserTag }
    | { kind: 'delete'; tag: UserTag }
    | null;

const formatError = (error: unknown) => (
    error instanceof Error && error.message ? error.message : String(error || '未知错误')
);

const TagsPage: React.FC<TagsPageProps> = ({ onStatus, onTagsMutated }) => {
    const tags = useSyncExternalStore(subscribeUserTags, getUserTagsSnapshot);
    const [openMenu, setOpenMenu] = useState('');
    const [prompt, setPrompt] = useState<PendingPrompt>(null);
    const [promptValue, setPromptValue] = useState('');
    const menuRef = useRef<HTMLDivElement | null>(null);

    useEffect(() => {
        void loadUserTags(true);
    }, []);

    useEffect(() => {
        if (!openMenu) {
            return;
        }
        const handlePointerDown = (event: MouseEvent) => {
            if (!menuRef.current?.contains(event.target as Node)) {
                setOpenMenu('');
            }
        };
        document.addEventListener('mousedown', handlePointerDown);
        return () => document.removeEventListener('mousedown', handlePointerDown);
    }, [openMenu]);

    const colors = useMemo(() => buildCategoryColorMap(tags), [tags]);

    const sections = useMemo(() => {
        const byCategory = new Map<string, UserTag[]>();
        tags.forEach((tag) => {
            const group = byCategory.get(tag.category) || [];
            group.push(tag);
            byCategory.set(tag.category, group);
        });
        const named = Array.from(byCategory.keys()).filter((category) => category !== '').sort();
        return {
            named: named.map((category) => ({ category, tags: byCategory.get(category) || [] })),
            uncategorized: byCategory.get('') || [],
        };
    }, [tags]);

    const afterMutation = useCallback((message: string) => {
        invalidateUserTags();
        onTagsMutated?.();
        onStatus?.(message, 'info');
    }, [onStatus, onTagsMutated]);

    const openPrompt = (next: NonNullable<PendingPrompt>) => {
        setOpenMenu('');
        setPrompt(next);
        if (next.kind === 'renameCategory') {
            setPromptValue(next.category);
        } else if (next.kind === 'rename') {
            setPromptValue(next.tag.name);
        } else if (next.kind === 'move') {
            setPromptValue(next.tag.category);
        } else {
            setPromptValue('');
        }
    };

    const commitPrompt = async () => {
        if (!prompt) {
            return;
        }
        try {
            // 分组不是实体，只是 Tag.Category 上的一个字符串：改组名 = 把组里每个标签挪过去
            if (prompt.kind === 'renameCategory') {
                const category = promptValue.trim();
                if (category === prompt.category) {
                    setPrompt(null);
                    return;
                }
                for (const member of prompt.tags) {
                    await MoveMyTag(member.id, category);
                }
                afterMutation(category
                    ? `已把分组「${prompt.category}」改名为「${category}」`
                    : `已解散分组「${prompt.category}」`);
                setPrompt(null);
                return;
            }

            const { kind, tag } = prompt;
            if (kind === 'rename') {
                const name = promptValue.trim();
                if (!name || name === tag.name) {
                    setPrompt(null);
                    return;
                }
                await RenameMyTag(tag.id, name);
                afterMutation(`已把「${tag.name}」改名为「${name}」`);
            } else if (kind === 'move') {
                await MoveMyTag(tag.id, promptValue.trim());
                afterMutation(promptValue.trim()
                    ? `已把「${tag.name}」移到「${promptValue.trim()}」`
                    : `已把「${tag.name}」移出分组`);
            } else if (kind === 'merge') {
                const target = tags.find((item) => item.id === promptValue);
                if (!target) {
                    setPrompt(null);
                    return;
                }
                await MergeMyTags(tag.id, target.id);
                afterMutation(`已把「${tag.name}」合并到「${target.name}」`);
            } else {
                await DeleteMyTag(tag.id);
                afterMutation(`已删除标签「${tag.name}」`);
            }
        } catch (error) {
            console.error(error);
            onStatus?.(formatError(error), 'error');
        }
        setPrompt(null);
    };

    const renderMenu = (tag: UserTag) => (
        <div className="navi-tag-menu" ref={menuRef} role="menu">
            <button type="button" onClick={() => openPrompt({ kind: 'rename', tag })}>
                <Pencil size={13} /><span>重命名</span>
            </button>
            <button type="button" onClick={() => openPrompt({ kind: 'merge', tag })}>
                <Merge size={13} /><span>合并到…</span>
            </button>
            <button type="button" onClick={() => openPrompt({ kind: 'move', tag })}>
                <FolderInput size={13} /><span>移到分组</span>
            </button>
            <div className="navi-tag-menu-sep" />
            <button type="button" className="danger" onClick={() => openPrompt({ kind: 'delete', tag })}>
                <Trash2 size={13} /><span>删除标签</span>
            </button>
        </div>
    );

    const renderRow = (tag: UserTag) => (
        <div className="navi-tag-row" key={tag.id}>
            <span className="navi-tag-row-name" title={tag.name}>{tag.name}</span>
            <span className="navi-tag-row-count">{tag.count} 部</span>
            <div className="navi-tag-row-menu-anchor">
                <button
                    type="button"
                    className={`navi-tag-row-more ${openMenu === tag.id ? 'on' : ''}`.trim()}
                    title="更多"
                    aria-label={`${tag.name} 的更多操作`}
                    onClick={() => setOpenMenu(openMenu === tag.id ? '' : tag.id)}
                >
                    <MoreHorizontal size={14} />
                </button>
                {openMenu === tag.id && renderMenu(tag)}
            </div>
        </div>
    );

    return (
        <div className="navi-tags-page">
            <div className="navi-tags-body">
                {tags.length === 0 && (
                    <div className="navi-tags-empty">
                        还没有标签。在详情页或海报墙上打第一个标签，它就会出现在这里。
                    </div>
                )}

                {sections.named.map((section) => (
                    <div className="navi-tags-section" key={section.category}>
                        <div className="navi-tags-section-head">
                            <span className="navi-tag-dot lg" style={{ background: categoryColor(colors, section.category) }} />
                            <span className="navi-tags-section-title">{section.category}</span>
                            <span className="navi-tags-section-count">{section.tags.length} 个标签</span>
                            <button
                                type="button"
                                className="navi-tags-section-rename"
                                title="重命名分组"
                                aria-label={`重命名分组 ${section.category}`}
                                onClick={() => openPrompt({
                                    kind: 'renameCategory',
                                    category: section.category,
                                    tags: section.tags,
                                })}
                            >
                                <Pencil size={12} />
                            </button>
                        </div>
                        <div className="navi-tags-rows">{section.tags.map(renderRow)}</div>
                    </div>
                ))}

                {sections.uncategorized.length > 0 && (
                    <div className={`navi-tags-section ${sections.named.length > 0 ? 'divided' : ''}`.trim()}>
                        <div className="navi-tags-section-head">
                            <span className="navi-tag-dot lg" style={{ background: UNCATEGORIZED_DOT }} />
                            <span className="navi-tags-section-title muted">{UNCATEGORIZED_LABEL}</span>
                            <span className="navi-tags-section-count">{sections.uncategorized.length} 个标签</span>
                        </div>
                        {/* 未分组通常最多，行式太长，改成 chip 平铺 */}
                        <div className="navi-tags-chips">
                            {sections.uncategorized.map((tag) => (
                                <div className="navi-tag-chip-anchor" key={tag.id}>
                                    <button
                                        type="button"
                                        className={`navi-tag-chip ${openMenu === tag.id ? 'on' : ''}`.trim()}
                                        onClick={() => setOpenMenu(openMenu === tag.id ? '' : tag.id)}
                                    >
                                        {tag.name}
                                        <span className="navi-tag-chip-count">{tag.count}</span>
                                    </button>
                                    {openMenu === tag.id && renderMenu(tag)}
                                </div>
                            ))}
                        </div>
                        <div className="navi-tags-note">
                            分组是可选的。不建分组也能用，只是筛选面板里全落在「我的标签」一组下
                        </div>
                    </div>
                )}
            </div>

            {prompt && (
                <div className="modal-overlay" onClick={() => setPrompt(null)}>
                    <div
                        className="confirm-modal"
                        onClick={(event) => event.stopPropagation()}
                        role="dialog"
                        aria-modal="true"
                        aria-labelledby="tag-prompt-title"
                    >
                        <div className="confirm-modal-header">
                            <span id="tag-prompt-title">
                                {prompt.kind === 'renameCategory' && `重命名分组「${prompt.category}」`}
                                {prompt.kind === 'rename' && `重命名「${prompt.tag.name}」`}
                                {prompt.kind === 'move' && `把「${prompt.tag.name}」移到分组`}
                                {prompt.kind === 'merge' && `把「${prompt.tag.name}」合并到`}
                                {prompt.kind === 'delete' && `删除标签「${prompt.tag.name}」`}
                            </span>
                            <button type="button" className="confirm-modal-close" onClick={() => setPrompt(null)} aria-label="关闭">
                                <X size={14} />
                            </button>
                        </div>

                        <div className="confirm-modal-body">
                            {prompt.kind === 'delete' && (
                                <div className="confirm-modal-text">
                                    {prompt.tag.count} 部片子会失去这个标签，影片本身不动。
                                </div>
                            )}
                            {prompt.kind === 'merge' && (
                                <>
                                    <div className="confirm-modal-text">
                                        「{prompt.tag.name}」的 {prompt.tag.count} 部片子会挂到目标标签上，这个标签随后消失。
                                    </div>
                                    <select
                                        className="navi-tag-prompt-input"
                                        value={promptValue}
                                        onChange={(event) => setPromptValue(event.target.value)}
                                    >
                                        <option value="">选择目标标签…</option>
                                        {tags.filter((tag) => tag.id !== prompt.tag.id).map((tag) => (
                                            <option key={tag.id} value={tag.id}>{tag.name}（{tag.count} 部）</option>
                                        ))}
                                    </select>
                                </>
                            )}
                            {(prompt.kind === 'rename' || prompt.kind === 'renameCategory') && (
                                <>
                                    {prompt.kind === 'renameCategory' && (
                                        <div className="confirm-modal-text">
                                            组里的 {prompt.tags.length} 个标签会一起挪过去，留空就是移出分组。
                                        </div>
                                    )}
                                    <input
                                        className="navi-tag-prompt-input"
                                        value={promptValue}
                                        autoFocus
                                        onChange={(event) => setPromptValue(event.target.value)}
                                        onKeyDown={(event) => event.key === 'Enter' && void commitPrompt()}
                                    />
                                </>
                            )}
                            {prompt.kind === 'move' && (
                                <>
                                    <div className="confirm-modal-text">
                                        分组名由你自己起，留空就是「{UNCATEGORIZED_LABEL}」。
                                    </div>
                                    <input
                                        className="navi-tag-prompt-input"
                                        value={promptValue}
                                        autoFocus
                                        placeholder="分组名"
                                        list="navi-tag-categories"
                                        onChange={(event) => setPromptValue(event.target.value)}
                                        onKeyDown={(event) => event.key === 'Enter' && void commitPrompt()}
                                    />
                                    <datalist id="navi-tag-categories">
                                        {sections.named.map((section) => (
                                            <option key={section.category} value={section.category} />
                                        ))}
                                    </datalist>
                                </>
                            )}
                        </div>

                        <div className="confirm-modal-actions">
                            <button type="button" className="confirm-modal-btn ghost" onClick={() => setPrompt(null)}>
                                取消
                            </button>
                            {/* 删除也用中性确认键，不是红色 */}
                            <button
                                type="button"
                                className="confirm-modal-btn primary"
                                disabled={prompt.kind === 'merge' && promptValue === ''}
                                onClick={() => void commitPrompt()}
                            >
                                {prompt.kind === 'delete' ? '删除' : '确定'}
                            </button>
                        </div>
                    </div>
                </div>
            )}
        </div>
    );
};

export default TagsPage;
