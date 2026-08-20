import React, { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react';
import { EyeOff, HeartOff } from 'lucide-react';
import { GetMediaListFiltered } from "../../wailsjs/go/main/App";
import MediaCard from './MediaCard';
import TagPicker from './TagPicker';
import StarRating from './StarRating';
import { prefetchMediaDetailCacheEntry, seedMediaDetailCache } from '../utils/mediaDetailCache';
import {
    createLatestRequestGate,
    finishPageLoading,
    getMediaAtIndex,
    getPagesForVisibleRange,
    getVirtualGridWindow,
    isPageLoadingForGeneration,
    markPageLoading,
    MEDIA_PAGE_SIZE,
    putMediaPage,
    requestMediaPage,
    type MediaPageQuery,
} from '../utils/mediaPagination';
import { markComponentRender } from '../utils/performanceDiagnostics';
import { shouldInvalidateMediaPagination } from '../utils/mediaPlaybackState';
import {
    buildCategoryColorMap,
    EMPTY_USER_FILTER,
    getUserTagsSnapshot,
    isUserFilterEmpty,
    invalidateUserTags,
    loadUserTags,
    subscribeUserTags,
    type UserFilter,
} from '../utils/userTags';
import { BatchSetMyRating } from "../../wailsjs/go/main/App";
import { Tag, X } from 'lucide-react';
import type { StatusKind } from '../types/status';

interface MediaGridProps {
    libraryId: string;
    keyword: string;
    sortField: string;
    sortOrder: 'asc' | 'desc';
    layoutVersion?: number;
    refreshVersion?: number;
    filter?: { type: string; value: string; label: string } | null;
    userFilter?: UserFilter;
    onClearUserFilter?: () => void;
    onSelectMedia: (media: any) => void;
    onCountChange?: (count: number) => void;
    onQuickPlayStatus?: (message: string, kind?: StatusKind) => void;
    onMediaChange?: (media: any) => void;
    initialScrollTop?: number;
    onScrollPositionChange?: (scrollTop: number) => void;
    mutation?: MediaGridMutation | null;
}

export type MediaGridMutation =
    | { type: 'merge'; media: any; changedFields?: string[] }
    | { type: 'remove'; mediaId: string };

const MEDIA_CARD_WIDTH = 192;
const MEDIA_CARD_HEIGHT = 330;
const MEDIA_GRID_MIN_GAP = 24;
const MEDIA_GRID_MAX_GAP = 36;
const MEDIA_GRID_ROW_GAP = 26;
const MEDIA_GRID_HORIZONTAL_PADDING = 56;
const VIRTUAL_OVERSCAN_ROWS = 2;
const SCROLL_NOTIFY_MS = 120;

export const getMediaListCacheKey = (
    libraryId: string,
    keyword: string,
    sortField: string,
    sortOrder: 'asc' | 'desc',
    filterType: string,
    filterValue: string,
    userFilter?: UserFilter,
) => JSON.stringify([
    libraryId, keyword.trim(), sortField, sortOrder, filterType, filterValue,
    serializeUserFilter(userFilter),
]);

// 用户条件参与缓存键和请求去重键，否则换一套筛选会读到上一套的缓存页。
export const serializeUserFilter = (userFilter?: UserFilter) => JSON.stringify([
    userFilter?.scores || [],
    userFilter?.tag_groups || [],
]);

// 空的已看 / 收藏页给一个说明为什么空、怎么填满的空状态，而不是一句报错似的灰字。
const EMPTY_LIST_STATES: Record<string, { Icon: typeof EyeOff; title: string; hint: string }> = {
    watched: {
        Icon: EyeOff,
        title: '还没有标记过已看',
        hint: '在任意作品的详情页点「标记已看」，它就会出现在这里',
    },
    favorite: {
        Icon: HeartOff,
        title: '还没有收藏任何作品',
        hint: '在任意作品的详情页点「收藏」，它就会出现在这里',
    },
};

const matchesActiveFilter = (media: any, filterType: string, filterValue: string) => {
    switch (filterType) {
        case 'favorite':
            return Boolean(media?.is_favorite);
        case 'watched':
            return Boolean(media?.is_watched);
        case 'unwatched':
            return !media?.is_watched;
        case 'media_type':
            return media?.media_type === filterValue;
        case 'series':
            return media?.series_id === filterValue;
        default:
            return true;
    }
};

const MediaGrid: React.FC<MediaGridProps> = ({
    libraryId,
    keyword,
    sortField,
    sortOrder,
    layoutVersion = 0,
    refreshVersion = 0,
    filter,
    userFilter = EMPTY_USER_FILTER,
    onClearUserFilter,
    onSelectMedia,
    onCountChange,
    onQuickPlayStatus,
    onMediaChange,
    initialScrollTop = 0,
    onScrollPositionChange,
    mutation = null,
}) => {
    markComponentRender('MediaGrid');
    const filterType = filter?.type || '';
    const filterValue = filter?.value || '';
    const serializedUserFilter = useMemo(() => serializeUserFilter(userFilter), [userFilter]);
    // loadPage 是 useCallback 出来的，把最新的用户条件放在 ref 里避免每次重建请求闭包
    const activeUserFilterRef = useRef(userFilter);
    activeUserFilterRef.current = userFilter;

    const userTags = useSyncExternalStore(subscribeUserTags, getUserTagsSnapshot);
    const categoryColors = useMemo(() => buildCategoryColorMap(userTags), [userTags]);
    const [selectedIDs, setSelectedIDs] = useState<ReadonlySet<string>>(() => new Set());
    const [tagPickerTarget, setTagPickerTarget] = useState<{ mediaIDs: string[]; rect: DOMRect; label: string } | null>(null);

    useEffect(() => {
        void loadUserTags();
    }, []);
    const [layout, setLayout] = useState({ columns: 4, gap: 20, justify: 'start' });
    const [viewportHeight, setViewportHeight] = useState(0);
    const [virtualScrollTop, setVirtualScrollTop] = useState(initialScrollTop);
    const [pages, setPages] = useState<Map<number, any[]>>(() => new Map());
    const [total, setTotal] = useState(0);
    const [isInitialLoading, setIsInitialLoading] = useState(true);
    const [loadingPages, setLoadingPages] = useState<Map<number, number>>(() => new Map());
    const [error, setError] = useState('');
    const [showingStaleResults, setShowingStaleResults] = useState(false);
    const containerRef = useRef<HTMLDivElement>(null);
    const latestScrollTopRef = useRef(initialScrollTop);
    const pendingRestoreRef = useRef<number | null>(initialScrollTop);
    const requestGateRef = useRef(createLatestRequestGate());
    const queryKeyRef = useRef('');
    const lastUsableRef = useRef<{ libraryId: string; pages: Map<number, any[]>; total: number } | null>(null);
    const lastSuccessfulLibraryRef = useRef('');
    const focusedMediaIDRef = useRef('');
    const pagesRef = useRef(pages);
    const totalRef = useRef(total);
    const loadingPagesRef = useRef(loadingPages);
    const visiblePagesRef = useRef<number[]>([1]);
    const scrollFrameRef = useRef<number | null>(null);
    const scrollNotifyTimerRef = useRef<number | null>(null);
    const onScrollPositionChangeRef = useRef(onScrollPositionChange);
    const onCountChangeRef = useRef(onCountChange);

    useEffect(() => {
        pagesRef.current = pages;
    }, [pages]);
    useEffect(() => {
        totalRef.current = total;
    }, [total]);
    useEffect(() => {
        loadingPagesRef.current = loadingPages;
    }, [loadingPages]);
    useEffect(() => {
        onScrollPositionChangeRef.current = onScrollPositionChange;
        onCountChangeRef.current = onCountChange;
    }, [onCountChange, onScrollPositionChange]);

    const normalizedKeyword = keyword.trim();
    const queryKey = getMediaListCacheKey(libraryId, normalizedKeyword, sortField, sortOrder, filterType, filterValue);
    const baseQuery = useMemo<Omit<MediaPageQuery, 'page'>>(() => ({
        libraryId,
        pageSize: MEDIA_PAGE_SIZE,
        searchTerm: normalizedKeyword,
        mediaType: filterType === 'media_type' ? filterValue : '',
        sortBy: sortField,
        sortOrder,
        favorite: filterType === 'favorite' ? true : null,
        watched: filterType === 'watched' ? true : filterType === 'unwatched' ? false : null,
        filterType,
        filterValue,
        userFilter: serializedUserFilter,
    }), [filterType, filterValue, libraryId, normalizedKeyword, serializedUserFilter, sortField, sortOrder]);

    const beginRequestGeneration = useCallback(() => {
        const generation = requestGateRef.current.next();
        const empty = new Map<number, number>();
        loadingPagesRef.current = empty;
        setLoadingPages(empty);
        return generation;
    }, []);

    const loadPage = useCallback((page: number, generation: number, force = false) => {
        if (!force && (pagesRef.current.has(page) || isPageLoadingForGeneration(loadingPagesRef.current, page, generation))) {
            return;
        }

        const startedLoading = markPageLoading(loadingPagesRef.current, page, generation);
        loadingPagesRef.current = startedLoading;
        setLoadingPages(startedLoading);

        const query: MediaPageQuery = { ...baseQuery, page };
        void requestMediaPage(query, async (normalizedQuery) => {
            const response: any = await GetMediaListFiltered(
                normalizedQuery.libraryId,
                normalizedQuery.page,
                normalizedQuery.pageSize,
                normalizedQuery.sortBy,
                normalizedQuery.sortOrder,
                normalizedQuery.searchTerm,
                normalizedQuery.filterType,
                normalizedQuery.filterValue,
                activeUserFilterRef.current as any,
            );
            return {
                items: Array.isArray(response?.items) ? response.items : [],
                total: Number.isFinite(Number(response?.total)) ? Math.max(0, Number(response.total)) : 0,
                page: Number.isFinite(Number(response?.page)) ? Math.max(1, Number(response.page)) : normalizedQuery.page,
                pageSize: normalizedQuery.pageSize,
            };
        }, requestGateRef.current.requestScope(generation)).then((response) => {
            if (!requestGateRef.current.accepts(generation)) {
                return;
            }

            const totalPages = Math.max(1, Math.ceil(response.total / response.pageSize));
            if (response.page > totalPages && response.total > 0) {
                loadPage(totalPages, generation, true);
                return;
            }
            if (response.items.length > response.pageSize) {
                throw new Error('后端返回条数超过分页上限');
            }
            if (response.page < totalPages && response.items.length < response.pageSize) {
                throw new Error('后端返回了不完整的媒体分页结果');
            }

            setPages((current) => putMediaPage(current, response.page, response.items, visiblePagesRef.current));
            setTotal(response.total);
            setError('');
            setShowingStaleResults(false);
            setIsInitialLoading(false);
            lastSuccessfulLibraryRef.current = baseQuery.libraryId;
            onCountChangeRef.current?.(response.total);
        }).catch((reason) => {
            if (!requestGateRef.current.accepts(generation)) {
                return;
            }
            console.error(reason);
            setError(reason instanceof Error && reason.message ? reason.message : '媒体列表加载失败');
            const fallback = lastUsableRef.current;
            if (pagesRef.current.size === 0 && fallback?.libraryId === baseQuery.libraryId) {
                pagesRef.current = fallback.pages;
                totalRef.current = fallback.total;
                setPages(fallback.pages);
                setTotal(fallback.total);
                setShowingStaleResults(true);
            }
            setIsInitialLoading(false);
        }).finally(() => {
            if (!requestGateRef.current.accepts(generation)) {
                return;
            }
            const finishedLoading = finishPageLoading(loadingPagesRef.current, page, generation);
            if (finishedLoading !== loadingPagesRef.current) {
                loadingPagesRef.current = finishedLoading;
                setLoadingPages(finishedLoading);
            }
        });
    }, [baseQuery]);

    useEffect(() => {
        const queryChanged = queryKeyRef.current !== queryKey;
        queryKeyRef.current = queryKey;
        const generation = beginRequestGeneration();

        if (queryChanged) {
            if (pagesRef.current.size > 0) {
                lastUsableRef.current = {
                    libraryId: lastSuccessfulLibraryRef.current,
                    pages: pagesRef.current,
                    total: totalRef.current,
                };
            }
            pagesRef.current = new Map();
            totalRef.current = 0;
            setPages(new Map());
            setTotal(0);
            setError('');
            setShowingStaleResults(false);
            setIsInitialLoading(true);
            pendingRestoreRef.current = initialScrollTop;
            latestScrollTopRef.current = initialScrollTop;
            setVirtualScrollTop(initialScrollTop);
            loadPage(1, generation, true);
            return;
        }

        const refreshPages = visiblePagesRef.current.length > 0 ? visiblePagesRef.current : [1];
        refreshPages.forEach((page) => loadPage(page, generation, true));
    }, [beginRequestGeneration, initialScrollTop, loadPage, queryKey, refreshVersion]);

    useEffect(() => () => {
        requestGateRef.current.dispose();
        if (scrollFrameRef.current !== null) {
            window.cancelAnimationFrame(scrollFrameRef.current);
        }
        if (scrollNotifyTimerRef.current !== null) {
            window.clearTimeout(scrollNotifyTimerRef.current);
        }
        onScrollPositionChangeRef.current?.(latestScrollTopRef.current);
    }, []);

    const updateLayout = useCallback(() => {
        if (!containerRef.current) {
            return;
        }
        const containerWidth = containerRef.current.clientWidth - MEDIA_GRID_HORIZONTAL_PADDING;
        const columns = Math.max(1, Math.floor((containerWidth + MEDIA_GRID_MIN_GAP) / (MEDIA_CARD_WIDTH + MEDIA_GRID_MIN_GAP)));
        const rawGap = columns > 1 ? (containerWidth - columns * MEDIA_CARD_WIDTH) / (columns - 1) : 0;
        const gap = Math.min(Math.max(rawGap, MEDIA_GRID_MIN_GAP), MEDIA_GRID_MAX_GAP);
        const justify = columns > 1 ? 'space-between' : 'start';
        setLayout((current) => current.columns === columns && current.gap === gap && current.justify === justify
            ? current
            : { columns, gap, justify });
        const nextHeight = containerRef.current.clientHeight || 0;
        setViewportHeight((current) => current === nextHeight ? current : nextHeight);
    }, []);

    useLayoutEffect(() => {
        let frame = 0;
        const schedule = () => {
            window.cancelAnimationFrame(frame);
            frame = window.requestAnimationFrame(updateLayout);
        };
        const observer = new ResizeObserver(schedule);
        if (containerRef.current) {
            observer.observe(containerRef.current);
        }
        window.addEventListener('resize', schedule);
        schedule();
        return () => {
            observer.disconnect();
            window.removeEventListener('resize', schedule);
            window.cancelAnimationFrame(frame);
        };
    }, [layoutVersion, updateLayout]);

    useLayoutEffect(() => {
        if (isInitialLoading || pendingRestoreRef.current === null || !containerRef.current) {
            return;
        }
        const maxScrollTop = Math.max(containerRef.current.scrollHeight - containerRef.current.clientHeight, 0);
        const restored = Math.min(Math.max(0, pendingRestoreRef.current), maxScrollTop);
        containerRef.current.scrollTop = restored;
        latestScrollTopRef.current = restored;
        setVirtualScrollTop(restored);
        pendingRestoreRef.current = null;
        onScrollPositionChangeRef.current?.(restored);
    }, [isInitialLoading, layout.columns, total]);

    const queryIsChanging = queryKeyRef.current !== queryKey;
    const effectiveTotal = queryIsChanging ? 0 : total;
    const rowHeight = MEDIA_CARD_HEIGHT + MEDIA_GRID_ROW_GAP;
    const effectiveViewportHeight = viewportHeight > 0 ? viewportHeight : rowHeight;
    const { totalRows, startRow, startIndex, endIndex } = getVirtualGridWindow(
        effectiveTotal,
        layout.columns,
        virtualScrollTop,
        effectiveViewportHeight,
        rowHeight,
        VIRTUAL_OVERSCAN_ROWS,
    );
    const visiblePages = useMemo(
        () => getPagesForVisibleRange(startIndex, endIndex, effectiveTotal),
        [effectiveTotal, endIndex, startIndex],
    );
    visiblePagesRef.current = visiblePages;

    useEffect(() => {
        if (effectiveTotal <= 0 || showingStaleResults) {
            return;
        }
        const generation = requestGateRef.current.current();
        visiblePages.forEach((page) => loadPage(page, generation));
    }, [effectiveTotal, loadPage, showingStaleResults, visiblePages]);

    useEffect(() => {
        if (!mutation) {
            return;
        }
        const mediaID = mutation.type === 'merge'
            ? (typeof mutation.media?.id === 'string' ? mutation.media.id.trim() : '')
            : (typeof mutation.mediaId === 'string' ? mutation.mediaId.trim() : '');
        if (!mediaID) {
            return;
        }

        let found = false;
        let removedFromCurrentList = false;
        const actualChangedFields = new Set<string>();
        const nextPages = new Map<number, any[]>();
        pagesRef.current.forEach((items, page) => {
            const updated = items.flatMap((item) => {
                if (item?.id !== mediaID) {
                    return [item];
                }
                found = true;
                if (mutation.type === 'remove') {
                    removedFromCurrentList = true;
                    return [];
                }
                Object.keys(mutation.media).forEach((field) => {
                    if (!Object.is(item?.[field], mutation.media[field])) {
                        actualChangedFields.add(field);
                    }
                });
                const merged = { ...item, ...mutation.media };
                if (!matchesActiveFilter(merged, filterType, filterValue)) {
                    removedFromCurrentList = true;
                    return [];
                }
                return [merged];
            });
            nextPages.set(page, updated);
        });
        if (found) {
            pagesRef.current = nextPages;
            setPages(nextPages);
        }
        if (removedFromCurrentList) {
            const nextTotal = Math.max(0, totalRef.current - 1);
            totalRef.current = nextTotal;
            setTotal(nextTotal);
            onCountChangeRef.current?.(nextTotal);
        }

        const changedFields = found
            ? Array.from(actualChangedFields)
            : mutation.type === 'merge'
                ? (mutation.changedFields || Object.keys(mutation.media))
                : [];
        const membershipOrOrderMayChange = mutation.type === 'remove'
            || shouldInvalidateMediaPagination(changedFields, filterType, sortField);
        if (membershipOrOrderMayChange) {
            const generation = beginRequestGeneration();
            (visiblePagesRef.current.length > 0 ? visiblePagesRef.current : [1]).forEach((page) => loadPage(page, generation, true));
        }
    }, [beginRequestGeneration, filterType, filterValue, loadPage, mutation, sortField]);

    const visibleSlots = useMemo(() => Array.from({ length: Math.max(0, endIndex - startIndex) }, (_, offset) => {
        const index = startIndex + offset;
        return { index, media: getMediaAtIndex(pages, index) };
    }), [endIndex, pages, startIndex]);
    const visibleMediaIDs = useMemo(() => new Set(visibleSlots.flatMap((slot) => slot.media?.id ? [slot.media.id] : [])), [visibleSlots]);
    useEffect(() => {
        if (focusedMediaIDRef.current && !visibleMediaIDs.has(focusedMediaIDRef.current)) {
            focusedMediaIDRef.current = '';
            // 焦点已经移到网格之外（例如搜索框）时不要抢回来，只有卡片被虚拟列表移除才回收焦点。
            const active = document.activeElement;
            if (active && active !== document.body && !containerRef.current?.contains(active)) {
                return;
            }
            containerRef.current?.focus({ preventScroll: true });
        }
    }, [visibleMediaIDs]);
    const topOffset = startRow * rowHeight;
    const totalContentHeight = totalRows === 0 ? 0 : totalRows * MEDIA_CARD_HEIGHT + Math.max(0, totalRows - 1) * MEDIA_GRID_ROW_GAP;

    const handleSelectMedia = useCallback((media: any) => {
        seedMediaDetailCache(media);
        onScrollPositionChangeRef.current?.(latestScrollTopRef.current);
        onSelectMedia(media);
    }, [onSelectMedia]);
    const handlePrefetchMedia = useCallback((media: any) => {
        seedMediaDetailCache(media);
        prefetchMediaDetailCacheEntry(media.id);
    }, []);
    const handleFocusMedia = useCallback((mediaID: string) => {
        focusedMediaIDRef.current = mediaID;
    }, []);

    // 进多选有两条路：Ctrl 点选随时可加；已经在多选里时普通点也算选。
    // 两种情况下的动作是一样的，都是切换这一张。
    const handleToggleSelect = useCallback((mediaID: string) => {
        setSelectedIDs((current) => {
            const next = new Set(current);
            if (next.has(mediaID)) {
                next.delete(mediaID);
            } else {
                next.add(mediaID);
            }
            return next;
        });
    }, []);

    const clearSelection = useCallback(() => setSelectedIDs(new Set()), []);

    const handleOpenTagPicker = useCallback((media: any, anchor: HTMLElement) => {
        setTagPickerTarget({
            mediaIDs: [media.id],
            rect: anchor.getBoundingClientRect(),
            label: media.title || media.code || '这部影片',
        });
    }, []);

    // popover 浮在海报墙上，光靠位置分不清是哪张卡，来源卡要一直亮着
    const taggingMediaID = tagPickerTarget?.mediaIDs.length === 1 ? tagPickerTarget.mediaIDs[0] : '';

    const selectedMediaList = useMemo(() => {
        if (selectedIDs.size === 0) {
            return [] as any[];
        }
        const found: any[] = [];
        pages.forEach((items) => items.forEach((item) => {
            if (item && selectedIDs.has(item.id)) {
                found.push(item);
            }
        }));
        return found;
    }, [pages, selectedIDs]);

    // TagPicker 的三态勾选要知道选中的这批片子里有几部已经挂了某个标签
    const selectionAssigned = useMemo(() => {
        const counts: Record<string, number> = {};
        const wanted = new Set(tagPickerTarget?.mediaIDs || []);
        const source: any[] = [];
        pages.forEach((items) => items.forEach((item) => {
            if (item && wanted.has(item.id)) {
                source.push(item);
            }
        }));
        source.forEach((media) => {
            (Array.isArray(media?.my_tags) ? media.my_tags : []).forEach((tag: any) => {
                if (tag?.id) {
                    counts[tag.id] = (counts[tag.id] || 0) + 1;
                }
            });
        });
        return counts;
    }, [pages, tagPickerTarget]);

    const applyBatchRating = useCallback(async (score: number) => {
        const ids = Array.from(selectedIDs);
        if (ids.length === 0) {
            return;
        }
        try {
            await BatchSetMyRating(ids, score);
            selectedMediaList.forEach((media) => onMediaChange?.({ ...media, my_rating: score }));
        } catch (error) {
            console.error(error);
            onQuickPlayStatus?.('批量打分失败', 'error');
        }
    }, [onMediaChange, onQuickPlayStatus, selectedIDs, selectedMediaList]);
    const hasUserFilter = !isUserFilterEmpty(userFilter);
    const emptyListState = (normalizedKeyword || hasUserFilter) ? undefined : EMPTY_LIST_STATES[filterType];
    const retry = () => {
        setShowingStaleResults(false);
        const generation = beginRequestGeneration();
        const retryPages = visiblePagesRef.current.length > 0 ? visiblePagesRef.current : [1];
        retryPages.forEach((page) => loadPage(page, generation, true));
    };

    return (
        <div
            ref={containerRef}
            className="grid-container"
            tabIndex={-1}
            aria-label="媒体列表"
            aria-busy={isInitialLoading || loadingPages.size > 0}
            onScroll={(event) => {
                latestScrollTopRef.current = event.currentTarget.scrollTop;
                if (scrollFrameRef.current === null) {
                    scrollFrameRef.current = window.requestAnimationFrame(() => {
                        scrollFrameRef.current = null;
                        setVirtualScrollTop(latestScrollTopRef.current);
                    });
                }
                if (scrollNotifyTimerRef.current !== null) {
                    window.clearTimeout(scrollNotifyTimerRef.current);
                }
                scrollNotifyTimerRef.current = window.setTimeout(() => {
                    scrollNotifyTimerRef.current = null;
                    onScrollPositionChangeRef.current?.(latestScrollTopRef.current);
                }, SCROLL_NOTIFY_MS);
            }}
        >
            {isInitialLoading && pages.size === 0 && (
                <div className="grid-feedback loading" role="status">正在加载媒体内容...</div>
            )}
            {!isInitialLoading && total === 0 && !error && (
                emptyListState ? (
                    <div className="navi-empty-state" role="status">
                        <emptyListState.Icon size={26} strokeWidth={1.6} />
                        <div className="navi-empty-state-title">{emptyListState.title}</div>
                        <div className="navi-empty-state-hint">{emptyListState.hint}</div>
                    </div>
                ) : hasUserFilter ? (
                    // 库里有几千部、只是条件筛没了，得说清楚并给条出路
                    <div className="grid-feedback" role="status">
                        <span>没有符合当前条件的影片</span>
                        {onClearUserFilter && (
                            <button type="button" className="grid-feedback-action" onClick={onClearUserFilter}>
                                清空条件
                            </button>
                        )}
                    </div>
                ) : normalizedKeyword ? (
                    <div className="grid-feedback" role="status">没搜到「{normalizedKeyword}」</div>
                ) : filterType ? (
                    <div className="grid-feedback" role="status">没有符合当前筛选条件的媒体</div>
                ) : (
                    <div className="grid-feedback" role="status">当前媒体库为空</div>
                )
            )}
            {error && (
                <div className="grid-error-panel" role="alert">
                    <span>{showingStaleResults ? `${error}；当前显示上一次可用结果` : error}</span>
                    <button type="button" onClick={retry}>重试</button>
                </div>
            )}
            {total > 0 && (
                <>
                    {/* 总数在顶部「N 部 · 容量 · 上次扫描」里，加载中由骨架卡说明，这里不再另起浮字 */}
                    <div className="grid-virtual-spacer" style={{ height: `${Math.max(totalContentHeight, effectiveViewportHeight)}px` }}>
                        <div
                            className="grid-virtual-content"
                            style={{
                                transform: `translateY(${topOffset}px)`,
                                gridTemplateColumns: `repeat(${layout.columns}, ${MEDIA_CARD_WIDTH}px)`,
                                columnGap: `${layout.gap}px`,
                                justifyContent: layout.justify,
                                rowGap: `${MEDIA_GRID_ROW_GAP}px`,
                            }}
                        >
                            {visibleSlots.map(({ index, media }) => media ? (
                                <MediaCard
                                    key={media.id}
                                    media={media}
                                    onSelectMedia={handleSelectMedia}
                                    onQuickPlayStatus={onQuickPlayStatus}
                                    onPrefetchMedia={handlePrefetchMedia}
                                    onFocusMedia={handleFocusMedia}
                                    onMediaChange={onMediaChange}
                                    categoryColors={categoryColors}
                                    selected={selectedIDs.has(media.id)}
                                    selectionActive={selectedIDs.size > 0}
                                    onToggleSelect={handleToggleSelect}
                                    onOpenTagPicker={handleOpenTagPicker}
                                    tagging={media.id === taggingMediaID}
                                />
                            ) : (
                                <div key={`placeholder-${index}`} className="navi-card navi-card-placeholder" aria-hidden="true">
                                    <div className="navi-card-poster" />
                                    <div className="navi-card-title" />
                                    <div className="navi-card-meta" />
                                </div>
                            ))}
                        </div>
                    </div>
                </>
            )}

            {selectedIDs.size > 0 && (
                <div className="navi-batch-bar" role="toolbar" aria-label="批量操作">
                    <span className="navi-batch-count">已选 {selectedIDs.size} 部</span>
                    <span className="navi-batch-sep" />
                    <StarRating
                        value={0}
                        size={15}
                        emptyColor="rgba(255,255,255,.28)"
                        label="批量评分"
                        onChange={(score) => void applyBatchRating(score)}
                    />
                    <button
                        type="button"
                        className="navi-batch-btn"
                        onClick={(event) => setTagPickerTarget({
                            mediaIDs: Array.from(selectedIDs),
                            rect: event.currentTarget.getBoundingClientRect(),
                            label: `已选 ${selectedIDs.size} 部`,
                        })}
                    >
                        <Tag size={13} />
                        <span>打标签</span>
                    </button>
                    <button
                        type="button"
                        className="navi-batch-close"
                        aria-label="退出多选"
                        onClick={clearSelection}
                    >
                        <X size={14} />
                    </button>
                </div>
            )}

            {tagPickerTarget && (
                <TagPicker
                    mediaIDs={tagPickerTarget.mediaIDs}
                    assigned={selectionAssigned}
                    targetLabel={tagPickerTarget.label}
                    style={{
                        position: 'fixed',
                        left: Math.max(12, Math.min(tagPickerTarget.rect.left, window.innerWidth - 302)),
                        top: Math.min(tagPickerTarget.rect.bottom + 6, window.innerHeight - 320),
                    }}
                    onClose={() => setTagPickerTarget(null)}
                    onChanged={() => {
                        invalidateUserTags();
                        // 标签变化改的是 my_tags，客户端没法判断还符不符合筛选条件，直接重拉
                        const generation = beginRequestGeneration();
                        pagesRef.current = new Map();
                        setPages(new Map());
                        (visiblePagesRef.current.length > 0 ? visiblePagesRef.current : [1])
                            .forEach((page) => loadPage(page, generation, true));
                    }}
                />
            )}
        </div>
    );
};

export default React.memo(MediaGrid, (prev, next) => (
    prev.libraryId === next.libraryId
    && prev.keyword === next.keyword
    && prev.sortField === next.sortField
    && prev.sortOrder === next.sortOrder
    && prev.layoutVersion === next.layoutVersion
    && prev.refreshVersion === next.refreshVersion
    && (prev.filter?.type || '') === (next.filter?.type || '')
    && (prev.filter?.value || '') === (next.filter?.value || '')
    && serializeUserFilter(prev.userFilter) === serializeUserFilter(next.userFilter)
    && prev.onSelectMedia === next.onSelectMedia
    && prev.onCountChange === next.onCountChange
    && prev.onQuickPlayStatus === next.onQuickPlayStatus
    && prev.onMediaChange === next.onMediaChange
    && prev.mutation === next.mutation
));
