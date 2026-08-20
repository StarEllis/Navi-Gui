import React, { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react';
import { Check, Minus, Plus, Search } from 'lucide-react';
import { BatchAddTag, BatchRemoveTag, CreateMyTag } from '../../wailsjs/go/main/App';
import {
    buildCategoryColorMap,
    categoryColor,
    getUserTagsSnapshot,
    invalidateUserTags,
    loadUserTags,
    subscribeUserTags,
    type UserTag,
} from '../utils/userTags';

interface TagPickerProps {
    // 三个入口共用：详情页「+ 标签」传一个 id，多选浮条传一批
    mediaIDs: string[];
    // tagID -> mediaIDs 里有几部挂了这个标签，用来做三态勾选
    assigned: Record<string, number>;
    // 正在给谁打标签。popover 是浮在海报墙上的，不写清楚就会打到隔壁那张卡上
    targetLabel: string;
    style?: React.CSSProperties;
    onClose: () => void;
    onChanged: (assigned: Record<string, number>) => void;
}

const CREATE_INDEX = -1;

const TagPicker: React.FC<TagPickerProps> = ({ mediaIDs, assigned, targetLabel, style, onClose, onChanged }) => {
    const tags = useSyncExternalStore(subscribeUserTags, getUserTagsSnapshot);
    const [query, setQuery] = useState('');
    const [highlight, setHighlight] = useState(0);
    const [localAssigned, setLocalAssigned] = useState<Record<string, number>>(assigned);
    const [busy, setBusy] = useState(false);
    const rootRef = useRef<HTMLDivElement | null>(null);
    const inputRef = useRef<HTMLInputElement | null>(null);

    useEffect(() => {
        void loadUserTags();
    }, []);

    useEffect(() => {
        setLocalAssigned(assigned);
    }, [assigned]);

    useEffect(() => {
        inputRef.current?.focus();
    }, []);

    useEffect(() => {
        const handlePointerDown = (event: MouseEvent) => {
            if (!rootRef.current?.contains(event.target as Node)) {
                onClose();
            }
        };
        document.addEventListener('mousedown', handlePointerDown);
        return () => document.removeEventListener('mousedown', handlePointerDown);
    }, [onClose]);

    const colors = useMemo(() => buildCategoryColorMap(tags), [tags]);

    // 只做子串匹配：名字是用户自己起的，模糊匹配会猜错
    const trimmed = query.trim();
    const visible = useMemo(() => {
        if (!trimmed) {
            return tags;
        }
        const needle = trimmed.toLowerCase();
        return tags.filter((tag) => tag.name.toLowerCase().includes(needle));
    }, [tags, trimmed]);

    const hasExactMatch = useMemo(
        () => tags.some((tag) => tag.name === trimmed),
        [tags, trimmed],
    );
    const showCreateRow = trimmed !== '' && !hasExactMatch;

    useEffect(() => {
        setHighlight(visible.length > 0 ? 0 : CREATE_INDEX);
    }, [trimmed, visible.length]);

    const selectedCount = useMemo(
        () => Object.values(localAssigned).filter((count) => count > 0).length,
        [localAssigned],
    );

    const applyChange = useCallback(async (tag: UserTag, nextFullySelected: boolean) => {
        setBusy(true);
        try {
            if (nextFullySelected) {
                await BatchAddTag(mediaIDs, tag.id);
            } else {
                await BatchRemoveTag(mediaIDs, tag.id);
            }
            const next = { ...localAssigned, [tag.id]: nextFullySelected ? mediaIDs.length : 0 };
            setLocalAssigned(next);
            invalidateUserTags();
            onChanged(next);
        } catch (error) {
            console.error(error);
        } finally {
            setBusy(false);
        }
    }, [localAssigned, mediaIDs, onChanged]);

    const toggleTag = useCallback((tag: UserTag) => {
        if (busy) {
            return;
        }
        // 部分选中时再点一次是「补齐」，不是「取消」
        const current = localAssigned[tag.id] || 0;
        void applyChange(tag, current < mediaIDs.length);
    }, [applyChange, busy, localAssigned, mediaIDs.length]);

    const createAndAssign = useCallback(async () => {
        if (busy || trimmed === '') {
            return;
        }
        setBusy(true);
        try {
            const created: any = await CreateMyTag(trimmed, '');
            const tagID = typeof created?.id === 'string' ? created.id : '';
            if (tagID) {
                await BatchAddTag(mediaIDs, tagID);
                const next = { ...localAssigned, [tagID]: mediaIDs.length };
                setLocalAssigned(next);
                onChanged(next);
            }
            invalidateUserTags();
            // 回车之后输入框清空但 popover 不关，要能连着打 5 个
            setQuery('');
            inputRef.current?.focus();
        } catch (error) {
            console.error(error);
        } finally {
            setBusy(false);
        }
    }, [busy, localAssigned, mediaIDs, onChanged, trimmed]);

    const handleKeyDown = (event: React.KeyboardEvent) => {
        const rows = visible.length + (showCreateRow ? 1 : 0);
        if (event.key === 'Escape') {
            event.preventDefault();
            event.stopPropagation();
            onClose();
            return;
        }
        if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
            event.preventDefault();
            if (rows === 0) {
                return;
            }
            const step = event.key === 'ArrowDown' ? 1 : -1;
            const positions = [...visible.map((_, index) => index), ...(showCreateRow ? [CREATE_INDEX] : [])];
            const currentIndex = Math.max(0, positions.indexOf(highlight));
            setHighlight(positions[(currentIndex + step + positions.length) % positions.length]);
            return;
        }
        if (event.key === 'Enter') {
            event.preventDefault();
            if (highlight === CREATE_INDEX) {
                void createAndAssign();
                return;
            }
            const tag = visible[highlight];
            if (tag) {
                toggleTag(tag);
            }
        }
    };

    const renderCheck = (tag: UserTag) => {
        const count = localAssigned[tag.id] || 0;
        if (count >= mediaIDs.length && count > 0) {
            return <Check size={13} />;
        }
        if (count > 0) {
            return <Minus size={13} />;
        }
        return null;
    };

    return (
        <div
            className="navi-tag-picker"
            ref={rootRef}
            style={style}
            role="dialog"
            aria-label="打标签"
            onKeyDown={handleKeyDown}
            onClick={(event) => event.stopPropagation()}
        >
            <div className="navi-tag-picker-head">
                <span className="navi-tag-picker-head-label">打标签</span>
                <span className="navi-tag-picker-head-target" title={targetLabel}>{targetLabel}</span>
            </div>

            <div className="navi-tag-picker-search">
                <Search size={13} />
                <input
                    ref={inputRef}
                    type="text"
                    value={query}
                    placeholder="搜索或新建标签"
                    onChange={(event) => setQuery(event.target.value)}
                />
            </div>

            <div className="navi-tag-picker-list">
                {visible.map((tag, index) => (
                    <div
                        key={tag.id}
                        className={`navi-tag-picker-row ${index === highlight ? 'active' : ''} ${(localAssigned[tag.id] || 0) > 0 ? 'on' : ''}`.trim()}
                        role="button"
                        tabIndex={-1}
                        onMouseEnter={() => setHighlight(index)}
                        onClick={() => toggleTag(tag)}
                    >
                        <span className="navi-tag-dot" style={{ background: categoryColor(colors, tag.category) }} />
                        <span className="navi-tag-picker-name">{tag.name}</span>
                        <span className="navi-tag-picker-count">{tag.count}</span>
                        <span className="navi-tag-picker-check">{renderCheck(tag)}</span>
                    </div>
                ))}

                {visible.length === 0 && !showCreateRow && (
                    <div className="navi-tag-picker-empty">还没有标签，输入名字新建一个</div>
                )}

                {showCreateRow && (
                    <>
                        {visible.length > 0 && <div className="navi-tag-picker-sep" />}
                        <div
                            className={`navi-tag-picker-row ${highlight === CREATE_INDEX ? 'active' : ''}`.trim()}
                            role="button"
                            tabIndex={-1}
                            onMouseEnter={() => setHighlight(CREATE_INDEX)}
                            onClick={() => void createAndAssign()}
                        >
                            <Plus size={13} />
                            <span className="navi-tag-picker-name">新建「<b>{trimmed}</b>」</span>
                            <span className="navi-tag-picker-enter">↵</span>
                        </div>
                    </>
                )}
            </div>

            {mediaIDs.length === 1 && (
                <div className="navi-tag-picker-tip">Ctrl 点选卡片可多选，一次给多部打分或打标签</div>
            )}

            <div className="navi-tag-picker-foot">
                <span>↑↓ 选择 · ↵ 确认 · esc 关闭</span>
                <span>已选 {selectedCount}</span>
            </div>
        </div>
    );
};

export default TagPicker;
