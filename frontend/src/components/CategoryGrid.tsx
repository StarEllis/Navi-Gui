import React, { useEffect, useMemo, useRef, useState } from 'react';
import { Camera } from 'lucide-react';
import { normalizeSearchTerm } from '../utils/mediaSearch';
import { toLocalAssetUrl } from '../utils/media';

interface StatsItem {
    name: string;
    count: number;
    image: string;
    filter_value: string;
}

export interface CategoryStats {
    total: number;
    withImage: number;
    works: number;
}

export type CategorySortField = 'count' | 'name';
export type CategoryViewMode = 'grid' | 'list';

interface CategoryGridProps {
    type: 'directory' | 'actor' | 'genre' | 'series';
    libraryId: string;
    keyword?: string;
    refreshVersion?: number;
    sortField?: CategorySortField;
    sortOrder?: 'asc' | 'desc';
    viewMode?: CategoryViewMode;
    onSelect: (value: string, label: string) => void;
    onStatsChange?: (stats: CategoryStats) => void;
    fetchFn: (libId: string) => Promise<StatsItem[]>;
    onPickAvatarFile?: (personID: string, name: string) => void;
    onPickAvatarURL?: (personID: string, name: string) => void;
    onCycleAvatar?: (personID: string, name: string) => void;
    onClearAvatar?: (personID: string, name: string) => void;
}

type AvatarMenuState = {
    x: number;
    y: number;
    personID: string;
    name: string;
    hasImage: boolean;
} | null;

const AVATAR_MENU_WIDTH = 168;
const AVATAR_MENU_HEIGHT = 118;
const AVATAR_MENU_MARGIN = 8;

const clampAvatarMenuPosition = (x: number, y: number) => ({
    x: Math.max(AVATAR_MENU_MARGIN, Math.min(x, window.innerWidth - AVATAR_MENU_WIDTH - AVATAR_MENU_MARGIN)),
    y: Math.max(AVATAR_MENU_MARGIN, Math.min(y, window.innerHeight - AVATAR_MENU_HEIGHT - AVATAR_MENU_MARGIN)),
});

type CountBucket = {
    key: string;
    label: string;
    matches: (count: number) => boolean;
};

const COUNT_BUCKETS: CountBucket[] = [
    { key: 'all', label: '全部', matches: () => true },
    { key: 'heavy', label: '10 部以上', matches: (count) => count >= 10 },
    { key: 'mid', label: '5-9 部', matches: (count) => count >= 5 && count <= 9 },
    { key: 'light', label: '2-4 部', matches: (count) => count >= 2 && count <= 4 },
    { key: 'single', label: '仅 1 部', matches: (count) => count <= 1 },
];

const TOP_PEOPLE_COUNT = 5;

/** 无头像时的体面回退：用名字散列出一个稳定的色相，配首字色块。 */
const getFallbackTone = (name: string) => {
    let hash = 0;
    for (let index = 0; index < name.length; index += 1) {
        hash = (hash * 31 + name.charCodeAt(index)) % 360;
    }
    return {
        background: `hsl(${hash}, 22%, 17%)`,
        color: `hsl(${hash}, 34%, 74%)`,
    };
};

const getInitial = (name: string) => (name.trim() ? Array.from(name.trim())[0] : '?');

const PersonAvatar: React.FC<{ item: StatsItem; className: string }> = ({ item, className }) => {
    const tone = getFallbackTone(item.name);
    const imagePath = typeof item.image === 'string' ? item.image.trim() : '';

    return (
        <div className={className} style={{ background: tone.background }}>
            <span className="navi-person-initial" style={{ color: tone.color }}>
                {getInitial(item.name)}
            </span>
            {imagePath && (
                <img
                    src={toLocalAssetUrl(imagePath)}
                    alt=""
                    loading="lazy"
                    decoding="async"
                    onError={(event) => {
                        event.currentTarget.hidden = true;
                    }}
                />
            )}
        </div>
    );
};

const CategoryGrid: React.FC<CategoryGridProps> = ({
    type,
    libraryId,
    keyword = '',
    refreshVersion = 0,
    sortField = 'count',
    sortOrder = 'desc',
    viewMode = 'grid',
    onSelect,
    onStatsChange,
    fetchFn,
    onPickAvatarFile,
    onPickAvatarURL,
    onCycleAvatar,
    onClearAvatar,
}) => {
    const [items, setItems] = useState<StatsItem[]>([]);
    const [loading, setLoading] = useState(true);
    const loadedKeyRef = useRef('');
    const [activeBucket, setActiveBucket] = useState('all');
    const [avatarMenu, setAvatarMenu] = useState<AvatarMenuState>(null);
    const normalizedKeyword = normalizeSearchTerm(keyword);
    const avatarEditable = type === 'actor' && Boolean(onPickAvatarFile || onPickAvatarURL || onCycleAvatar || onClearAvatar);

    useEffect(() => {
        if (!avatarMenu) {
            return;
        }
        const close = () => setAvatarMenu(null);
        const closeOnEscape = (event: KeyboardEvent) => {
            if (event.key === 'Escape') {
                close();
            }
        };
        document.addEventListener('mousedown', close);
        document.addEventListener('keydown', closeOnEscape);
        window.addEventListener('resize', close);
        return () => {
            document.removeEventListener('mousedown', close);
            document.removeEventListener('keydown', closeOnEscape);
            window.removeEventListener('resize', close);
        };
    }, [avatarMenu]);

    // 右键任意演员、或点头像上的角标，都打开同一个菜单。
    const openAvatarMenu = (event: React.MouseEvent, item: StatsItem) => {
        if (!avatarEditable) {
            return;
        }
        event.preventDefault();
        event.stopPropagation();
        const position = clampAvatarMenuPosition(event.clientX, event.clientY);
        setAvatarMenu({
            ...position,
            personID: item.filter_value,
            name: item.name,
            hasImage: Boolean(typeof item.image === 'string' && item.image.trim()),
        });
    };

    const runAvatarAction = (action?: (personID: string, name: string) => void) => {
        if (!avatarMenu || !action) {
            return;
        }
        const { personID, name } = avatarMenu;
        setAvatarMenu(null);
        action(personID, name);
    };

    useEffect(() => {
        let active = true;
        // 扫描收尾后元数据补全还要跑几分钟，期间每隔几秒就推一次刷新。
        // 那种刷新只是重取同一份数据，不能把已经画出来的网格清空成加载态，
        // 否则整页在「正在加载...」和内容之间反复跳。只有切库/切类型才给加载态。
        const loadKey = `${libraryId}|${type}`;
        if (loadedKeyRef.current !== loadKey) {
            setLoading(true);
        }

        fetchFn(libraryId)
            .then((res) => {
                if (!active) {
                    return;
                }
                const nextItems = Array.isArray(res) ? [...res] : [];
                nextItems.sort((left, right) => {
                    if (right.count !== left.count) {
                        return right.count - left.count;
                    }
                    return left.name.localeCompare(right.name, 'zh-CN');
                });
                setItems(nextItems);
                loadedKeyRef.current = loadKey;
                setLoading(false);
            })
            .catch((error) => {
                console.error(error);
                if (!active) {
                    return;
                }
                setItems([]);
                setLoading(false);
            });

        return () => {
            active = false;
        };
    }, [fetchFn, libraryId, refreshVersion, type]);

    useEffect(() => {
        setActiveBucket('all');
    }, [libraryId, type]);

    useEffect(() => {
        onStatsChange?.({
            total: items.length,
            withImage: items.filter((item) => typeof item.image === 'string' && item.image.trim()).length,
            works: items.reduce((sum, item) => sum + (item.count || 0), 0),
        });
    }, [items, onStatsChange]);

    const bucket = COUNT_BUCKETS.find((option) => option.key === activeBucket) || COUNT_BUCKETS[0];
    // items 始终保持「作品数降序」作为基准（看得最多依赖它），排序只作用于下面的全部列表。
    const visibleItems = useMemo(() => {
        const filtered = items.filter((item) => (
            (!normalizedKeyword || normalizeSearchTerm(item.name).includes(normalizedKeyword))
            && bucket.matches(item.count || 0)
        ));
        const direction = sortOrder === 'asc' ? -1 : 1;
        return filtered.sort((left, right) => {
            if (sortField === 'name') {
                return right.name.localeCompare(left.name, 'zh-CN') * direction;
            }
            if (right.count !== left.count) {
                return (right.count - left.count) * direction;
            }
            return left.name.localeCompare(right.name, 'zh-CN');
        });
    }, [bucket, items, normalizedKeyword, sortField, sortOrder]);

    const topPeople = useMemo(
        () => (normalizedKeyword ? [] : items.slice(0, TOP_PEOPLE_COUNT)),
        [items, normalizedKeyword],
    );

    if (loading) {
        return <div className="navi-category-feedback">正在加载...</div>;
    }

    if (items.length === 0) {
        return <div className="navi-category-feedback">暂无数据</div>;
    }

    if (type !== 'actor') {
        return (
            <div className="navi-people">
                <div className="navi-genre-grid">
                    {visibleItems.map((item) => (
                        <button
                            key={item.filter_value || item.name}
                            type="button"
                            className="navi-genre-card"
                            onClick={() => onSelect(item.filter_value, item.name)}
                        >
                            <span title={item.name}>{item.name}</span>
                            <span className="navi-genre-count">{item.count}</span>
                        </button>
                    ))}
                </div>

                {visibleItems.length === 0 && (
                    <div className="navi-category-feedback">没有符合搜索条件的条目</div>
                )}
            </div>
        );
    }

    return (
        <div className="navi-people navi-people-actor">
            {topPeople.length > 0 && (
                <>
                    <div className="navi-section-head">
                        <span className="navi-section-title">看得最多</span>
                    </div>
                    <div className="navi-people-top">
                        {topPeople.map((item) => (
                            <button
                                key={`top-${item.filter_value || item.name}`}
                                type="button"
                                className="navi-people-top-card"
                                onClick={() => onSelect(item.filter_value, item.name)}
                                onContextMenu={(event) => openAvatarMenu(event, item)}
                            >
                                <PersonAvatar item={item} className="navi-people-top-avatar" />
                                <div className="navi-people-top-copy">
                                    <div className="navi-people-top-name" title={item.name}>{item.name}</div>
                                    <div className="navi-people-top-meta">{item.count} 部</div>
                                </div>
                            </button>
                        ))}
                    </div>
                </>
            )}

            <div className="navi-section-head">
                <span className="navi-section-title">全部演员</span>
                <span className="navi-section-count">{visibleItems.length}</span>
                <div className="navi-chip-row">
                    {COUNT_BUCKETS.map((option) => (
                        <button
                            key={option.key}
                            type="button"
                            className={`navi-chip ${option.key === activeBucket ? 'active' : ''}`.trim()}
                            onClick={() => setActiveBucket(option.key)}
                        >
                            {option.label}
                        </button>
                    ))}
                </div>
            </div>

            {viewMode === 'list' ? (
                <div className="navi-people-list">
                    {visibleItems.map((item) => (
                        <button
                            key={item.filter_value || item.name}
                            type="button"
                            className="navi-person-row"
                            onClick={() => onSelect(item.filter_value, item.name)}
                            onContextMenu={(event) => openAvatarMenu(event, item)}
                        >
                            <PersonAvatar item={item} className="navi-person-row-avatar" />
                            <span className="navi-person-row-name" title={item.name}>{item.name}</span>
                            <span className="navi-person-row-count">{item.count} 部</span>
                        </button>
                    ))}
                </div>
            ) : (
                <div className="navi-people-grid">
                    {visibleItems.map((item) => (
                        <div className="navi-person-slot" key={item.filter_value || item.name}>
                            <button
                                type="button"
                                className="navi-person"
                                onClick={() => onSelect(item.filter_value, item.name)}
                                onContextMenu={(event) => openAvatarMenu(event, item)}
                            >
                                <PersonAvatar item={item} className="navi-person-avatar" />
                                <div className="navi-person-copy">
                                    <div className="navi-person-name" title={item.name}>{item.name}</div>
                                    <div className="navi-person-count">{item.count}</div>
                                </div>
                            </button>

                            {avatarEditable && (
                                // 只覆盖头像那个正方形，其余区域点击穿透给卡片。
                                <div className="navi-person-avatar-anchor">
                                    <button
                                        type="button"
                                        className="navi-person-avatar-edit"
                                        title="设置头像"
                                        aria-label={`设置 ${item.name} 的头像`}
                                        onClick={(event) => openAvatarMenu(event, item)}
                                    >
                                        <Camera size={13} />
                                    </button>
                                </div>
                            )}
                        </div>
                    ))}
                </div>
            )}

            {avatarMenu && (
                <div
                    className="navi-avatar-menu"
                    style={{ left: avatarMenu.x, top: avatarMenu.y }}
                    role="menu"
                    aria-label={`${avatarMenu.name} 的头像`}
                    onMouseDown={(event) => event.stopPropagation()}
                    onContextMenu={(event) => event.preventDefault()}
                >
                    <div className="navi-avatar-menu-title" title={avatarMenu.name}>{avatarMenu.name}</div>
                    <button
                        type="button"
                        className="navi-avatar-menu-item"
                        role="menuitem"
                        onClick={() => runAvatarAction(onCycleAvatar)}
                    >
                        换一张头像
                    </button>
                    <button
                        type="button"
                        className="navi-avatar-menu-item"
                        role="menuitem"
                        onClick={() => runAvatarAction(onPickAvatarFile)}
                    >
                        设置头像…
                    </button>
                    <button
                        type="button"
                        className="navi-avatar-menu-item"
                        role="menuitem"
                        onClick={() => runAvatarAction(onPickAvatarURL)}
                    >
                        从图片链接设置…
                    </button>
                    <button
                        type="button"
                        className="navi-avatar-menu-item"
                        role="menuitem"
                        disabled={!avatarMenu.hasImage}
                        onClick={() => runAvatarAction(onClearAvatar)}
                    >
                        移除头像
                    </button>
                </div>
            )}

            {visibleItems.length === 0 && (
                <div className="navi-category-feedback">没有符合当前条件的演员</div>
            )}
        </div>
    );
};

export default CategoryGrid;
