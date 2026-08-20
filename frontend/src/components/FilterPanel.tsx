import React, { useCallback, useMemo, useState } from 'react';
import { Bookmark, ChevronRight, Star, Tags, X } from 'lucide-react';
import {
    buildCategoryColorMap,
    categoryColor,
    describeUserFilter,
    groupSelectedTags,
    RATING_LEVELS,
    scoreLabel,
    toggleScore,
    UNCATEGORIZED_LABEL,
    UNRATED_SCORE,
    type SavedFilter,
    type UserFilter,
    type UserTag,
} from '../utils/userTags';

export type FilterFacets = {
    tags: Record<string, number>;
    ratings: Record<string, number>;
    total: number;
};

const RATING_CHIPS: number[] = [...RATING_LEVELS, UNRATED_SCORE];

interface FilterPanelProps {
    filter: UserFilter;
    tags: ReadonlyArray<UserTag>;
    facets: FilterFacets | null;
    savedFilters: ReadonlyArray<SavedFilter>;
    onChange: (next: UserFilter) => void;
    onSaveCurrent: (name: string) => void;
    onApplySaved: (saved: SavedFilter) => void;
    onDeleteSaved: (id: string) => void;
    onOpenTagsPage: () => void;
}

export const FilterPanel: React.FC<FilterPanelProps> = ({
    filter,
    tags,
    facets,
    savedFilters,
    onChange,
    onSaveCurrent,
    onApplySaved,
    onDeleteSaved,
    onOpenTagsPage,
}) => {
    const [namingSave, setNamingSave] = useState(false);
    const [saveName, setSaveName] = useState('');
    const colors = useMemo(() => buildCategoryColorMap(tags), [tags]);
    const selected = useMemo(() => new Set(filter.tag_groups.flat()), [filter.tag_groups]);
    const selectedScores = useMemo(() => new Set(filter.scores), [filter.scores]);

    const sections = useMemo(() => {
        const byCategory = new Map<string, UserTag[]>();
        tags.forEach((tag) => {
            const group = byCategory.get(tag.category) || [];
            group.push(tag);
            byCategory.set(tag.category, group);
        });
        const named = Array.from(byCategory.keys()).filter((category) => category !== '').sort();
        const ordered = [...named, ...(byCategory.has('') ? [''] : [])];
        return ordered.map((category) => ({
            category,
            label: category === '' ? (named.length === 0 ? '我的标签' : UNCATEGORIZED_LABEL) : category,
            tags: byCategory.get(category) || [],
        }));
    }, [tags]);

    const toggleTag = useCallback((tagID: string) => {
        const next = new Set(selected);
        if (next.has(tagID)) {
            next.delete(tagID);
        } else {
            next.add(tagID);
        }
        onChange({ ...filter, tag_groups: groupSelectedTags(next, tags) });
    }, [filter, onChange, selected, tags]);

    const pickScore = useCallback((score: number) => {
        onChange(toggleScore(filter, score));
    }, [filter, onChange]);

    const clearAll = useCallback(() => {
        onChange({ scores: [], tag_groups: [] });
    }, [onChange]);

    const beginSave = () => {
        setSaveName(describeUserFilter(filter, tags) || '常用筛选');
        setNamingSave(true);
    };

    const commitSave = () => {
        const name = saveName.trim();
        if (name) {
            onSaveCurrent(name);
        }
        setNamingSave(false);
    };

    return (
        <div className="navi-filter-panel" role="dialog" aria-label="筛选">
            {savedFilters.length > 0 && (
                <div className="navi-filter-section">
                    <div className="navi-filter-section-head">
                        <span className="navi-filter-section-title">常用筛选</span>
                    </div>
                    <div className="navi-filter-chips">
                        {savedFilters.map((saved) => (
                            <span key={saved.id} className="navi-filter-chip saved">
                                <button type="button" className="navi-filter-chip-main" onClick={() => onApplySaved(saved)}>
                                    <Bookmark size={11} />
                                    {saved.name}
                                </button>
                                <button
                                    type="button"
                                    className="navi-filter-chip-x"
                                    title="删除常用筛选"
                                    aria-label={`删除常用筛选 ${saved.name}`}
                                    onClick={() => onDeleteSaved(saved.id)}
                                >
                                    <X size={11} />
                                </button>
                            </span>
                        ))}
                    </div>
                </div>
            )}

            <div className="navi-filter-section">
                <div className="navi-filter-section-head">
                    <span className="navi-filter-section-title">我的评分</span>
                    <span className="navi-filter-section-hint">组内多选 = 或</span>
                </div>
                <div className="navi-filter-chips">
                    {RATING_CHIPS.map((score) => {
                        const isActive = selectedScores.has(score);
                        const key = score === UNRATED_SCORE ? 'unrated' : String(score);
                        const count = facets ? Number(facets.ratings?.[key] ?? 0) : null;
                        // 会变 0 的选项直接置灰不可点，别让用户点出一个空列表
                        const disabled = count === 0 && !isActive;
                        return (
                            <button
                                key={key}
                                type="button"
                                className={`navi-filter-chip ${isActive ? 'on' : ''}`.trim()}
                                disabled={disabled}
                                aria-pressed={isActive}
                                onClick={() => pickScore(score)}
                            >
                                {score !== UNRATED_SCORE && <Star size={11} color="#e0a05a" fill="#e0a05a" />}
                                {scoreLabel(score)}
                                {count !== null && <span className="navi-filter-chip-count">{count}</span>}
                            </button>
                        );
                    })}
                </div>
            </div>

            {sections.map((section) => (
                <div className="navi-filter-section" key={section.category || '__uncategorized__'}>
                    <div className="navi-filter-section-head">
                        <span className="navi-filter-section-title">{section.label}</span>
                        {section.tags.length > 1 && <span className="navi-filter-section-hint">组内多选 = 或</span>}
                    </div>
                    <div className="navi-filter-chips">
                        {section.tags.map((tag) => {
                            const isActive = selected.has(tag.id);
                            const count = facets ? Number(facets.tags?.[tag.id] ?? 0) : null;
                            const disabled = count === 0 && !isActive;
                            return (
                                <button
                                    key={tag.id}
                                    type="button"
                                    className={`navi-filter-chip ${isActive ? 'on' : ''}`.trim()}
                                    disabled={disabled}
                                    aria-pressed={isActive}
                                    onClick={() => toggleTag(tag.id)}
                                >
                                    <span className="navi-tag-dot" style={{ background: categoryColor(colors, tag.category) }} />
                                    {tag.name}
                                    {count !== null && <span className="navi-filter-chip-count">{count}</span>}
                                </button>
                            );
                        })}
                    </div>
                </div>
            ))}

            {tags.length === 0 && (
                <div className="navi-filter-empty">还没有标签。在详情页或海报墙上打第一个标签，它就会出现在这里</div>
            )}

            <div className="navi-filter-foot">
                {namingSave ? (
                    <input
                        className="navi-filter-save-input"
                        value={saveName}
                        autoFocus
                        onChange={(event) => setSaveName(event.target.value)}
                        onBlur={commitSave}
                        onKeyDown={(event) => {
                            if (event.key === 'Enter') {
                                commitSave();
                            } else if (event.key === 'Escape') {
                                setNamingSave(false);
                            }
                        }}
                    />
                ) : (
                    <>
                        <span className="navi-filter-foot-count">
                            {facets ? `符合 ${facets.total} 部` : '统计中…'}
                        </span>
                        <div className="navi-filter-foot-actions">
                            <button type="button" className="navi-filter-foot-btn" onClick={clearAll}>清空</button>
                            <button type="button" className="navi-filter-foot-btn primary" onClick={beginSave}>
                                <Bookmark size={12} />
                                <span>存为常用</span>
                            </button>
                        </div>
                    </>
                )}
            </div>

            {/* 标签管理页的唯一入口：侧栏只表达「去哪」，标签是这里的东西 */}
            <button type="button" className="navi-filter-manage" onClick={onOpenTagsPage}>
                <Tags size={13} />
                <span>管理标签…</span>
                <ChevronRight size={13} />
            </button>
        </div>
    );
};

interface FilterConditionBarProps {
    filter: UserFilter;
    tags: ReadonlyArray<UserTag>;
    total: number | null;
    onChange: (next: UserFilter) => void;
}

// 顶栏下方常驻的条件条：面板关掉它也留着，这是「筛完忘了自己在什么筛选下」的唯一解。
export const FilterConditionBar: React.FC<FilterConditionBarProps> = ({ filter, tags, total, onChange }) => {
    const colors = useMemo(() => buildCategoryColorMap(tags), [tags]);
    const tagByID = useMemo(() => new Map(tags.map((tag) => [tag.id, tag])), [tags]);
    const selected = useMemo(() => filter.tag_groups.flat(), [filter.tag_groups]);

    const dropTag = (tagID: string) => {
        const next = new Set(selected);
        next.delete(tagID);
        onChange({ ...filter, tag_groups: groupSelectedTags(next, tags) });
    };

    return (
        <div className="navi-filter-bar">
            <span className="navi-filter-bar-label">筛选中</span>

            {/* 星级一档一个 chip，各自能单独摘掉，不用回面板 */}
            {filter.scores.map((score) => (
                <span className="navi-filter-bar-chip" key={`score-${score}`}>
                    {score !== UNRATED_SCORE && <Star size={11} color="#e0a05a" fill="#e0a05a" />}
                    {scoreLabel(score)}
                    <button
                        type="button"
                        aria-label={`取消 ${scoreLabel(score)}`}
                        onClick={() => onChange(toggleScore(filter, score))}
                    >
                        <X size={12} />
                    </button>
                </span>
            ))}

            {selected.map((tagID) => {
                const tag = tagByID.get(tagID);
                if (!tag) {
                    return null;
                }
                return (
                    <span className="navi-filter-bar-chip" key={tagID}>
                        <span className="navi-tag-dot" style={{ background: categoryColor(colors, tag.category) }} />
                        {tag.name}
                        <button type="button" aria-label={`取消 ${tag.name}`} onClick={() => dropTag(tagID)}>
                            <X size={12} />
                        </button>
                    </span>
                );
            })}

            {total !== null && <span className="navi-filter-bar-total">→ {total} 部</span>}

            <button
                type="button"
                className="navi-filter-bar-clear"
                onClick={() => onChange({ scores: [], tag_groups: [] })}
            >
                清空
            </button>
        </div>
    );
};
