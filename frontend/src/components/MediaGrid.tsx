import React, { startTransition, useDeferredValue, useEffect, useLayoutEffect, useRef, useState } from 'react';
import { GetMediaList } from "../../wailsjs/go/main/App";
import MediaCard from './MediaCard';
import {
    getCachedMediaListEntry,
    mergeMediaIntoCachedMediaLists,
    persistMediaListCache,
    removeMediaFromCachedMediaLists,
} from '../utils/persistentCache';
import { buildMediaSearchIndex, normalizeSearchField, normalizeSearchTerm } from '../utils/mediaSearch';
import { prefetchMediaDetailCacheEntry, seedMediaDetailCache } from '../utils/mediaDetailCache';

interface MediaGridProps {
    libraryId: string;
    keyword: string;
    sortField: string;
    sortOrder: 'asc' | 'desc';
    layoutVersion?: number;
    refreshVersion?: number;
    filter?: { type: string; value: string; label: string } | null;
    onSelectMedia: (media: any) => void;
    onCountChange?: (count: number) => void;
    onQuickPlayStatus?: (message: string) => void;
    initialScrollTop?: number;
    onScrollPositionChange?: (scrollTop: number) => void;
    mutation?: MediaGridMutation | null;
}

export type MediaGridMutation =
    | { type: 'merge'; media: any }
    | { type: 'remove'; mediaId: string };

type SearchableMediaItem = {
    media: any;
    searchIndex: string;
    codeIndex: string;
    titleIndex: string;
    actorIndex: string;
    genreIndex: string;
    metadataIndex: string;
    pathIndex: string;
};

const MEDIA_CARD_WIDTH = 178;
const MEDIA_CARD_HEIGHT = 297;
const MEDIA_GRID_MIN_GAP = 18;
const MEDIA_GRID_MAX_GAP = 24;
const MEDIA_GRID_ROW_GAP = 24;
const MEDIA_GRID_HORIZONTAL_PADDING = 48;
const VIRTUAL_OVERSCAN_ROWS = 2;

export const getMediaListCacheKey = (
    libraryId: string,
    keyword: string,
    sortField: string,
    sortOrder: 'asc' | 'desc',
    filterType: string,
    filterValue: string,
) => JSON.stringify([libraryId, keyword, sortField, sortOrder, filterType, filterValue]);

export const getMediaListBaseCacheKey = (
    libraryId: string,
    sortField: string,
    sortOrder: 'asc' | 'desc',
    filterType: string,
    filterValue: string,
) => JSON.stringify([libraryId, sortField, sortOrder, filterType, filterValue]);

const createSearchableMediaItems = (items: any[]): SearchableMediaItem[] => {
    if (!Array.isArray(items)) {
        return [];
    }

    return items
        .filter((item) => typeof item?.id === 'string' && item.id.trim().length > 0)
        .map((item) => ({
            media: item,
            searchIndex: buildMediaSearchIndex(item),
            codeIndex: normalizeSearchField(item.code),
            titleIndex: normalizeSearchTerm([item.title, item.orig_title].filter(Boolean).join('\n')),
            actorIndex: normalizeSearchField(item.actor),
            genreIndex: normalizeSearchField(item.genres),
            metadataIndex: normalizeSearchTerm([
                item.studio,
                item.maker,
                item.label,
                item.release_date_normalized,
                typeof item.year === 'number' && item.year > 0 ? String(item.year) : '',
            ].filter(Boolean).join('\n')),
            pathIndex: normalizeSearchField(item.file_path),
        }));
};

const getSearchPriority = (item: SearchableMediaItem, normalizedKeyword: string) => {
    if (!normalizedKeyword || !item.searchIndex.includes(normalizedKeyword)) {
        return null;
    }

    if (item.codeIndex === normalizedKeyword) {
        return 0;
    }
    if (item.codeIndex.startsWith(normalizedKeyword)) {
        return 1;
    }
    if (item.codeIndex.includes(normalizedKeyword)) {
        return 2;
    }
    if (item.titleIndex.includes(normalizedKeyword)) {
        return 3;
    }
    if (item.actorIndex.includes(normalizedKeyword)) {
        return 4;
    }
    if (item.genreIndex.includes(normalizedKeyword)) {
        return 5;
    }
    if (item.metadataIndex.includes(normalizedKeyword)) {
        return 6;
    }
    if (item.pathIndex.includes(normalizedKeyword)) {
        return 7;
    }

    return 8;
};

const FILTER_ONLY_SEARCH_TERMS = new Set([
    '4k',
    '8k',
    '2160p',
    '1080p',
    '720p',
    'hevc',
    'h265',
    'h 265',
    'h264',
    'h 264',
    'avc',
    '字幕',
    '中文字幕',
    '破解',
    '无码',
    '有码',
]);

const filterSearchableMediaItems = (items: SearchableMediaItem[], keyword: string) => {
    const normalizedKeyword = normalizeSearchTerm(keyword);
    if (!normalizedKeyword) {
        return items.map(({ media }) => media);
    }

    if (FILTER_ONLY_SEARCH_TERMS.has(normalizedKeyword)) {
        return items
            .filter((item) => item.searchIndex.includes(normalizedKeyword))
            .map(({ media }) => media);
    }

    return items
        .map((item, index) => ({
            media: item.media,
            index,
            priority: getSearchPriority(item, normalizedKeyword),
        }))
        .filter((item): item is { media: any; index: number; priority: number } => item.priority !== null)
        .sort((left, right) => left.priority - right.priority || left.index - right.index)
        .map(({ media }) => media);
};

const mergeSearchableMediaItem = (item: SearchableMediaItem, media: any): SearchableMediaItem => {
    const mergedMedia = {
        ...item.media,
        ...media,
    };

    return {
        media: mergedMedia,
        searchIndex: buildMediaSearchIndex(mergedMedia),
        codeIndex: normalizeSearchField(mergedMedia.code),
        titleIndex: normalizeSearchTerm([mergedMedia.title, mergedMedia.orig_title].filter(Boolean).join('\n')),
        actorIndex: normalizeSearchField(mergedMedia.actor),
        genreIndex: normalizeSearchField(mergedMedia.genres),
        metadataIndex: normalizeSearchTerm([
            mergedMedia.studio,
            mergedMedia.maker,
            mergedMedia.label,
            mergedMedia.release_date_normalized,
            typeof mergedMedia.year === 'number' && mergedMedia.year > 0 ? String(mergedMedia.year) : '',
        ].filter(Boolean).join('\n')),
        pathIndex: normalizeSearchField(mergedMedia.file_path),
    };
};

const MediaGrid: React.FC<MediaGridProps> = ({
    libraryId,
    keyword,
    sortField,
    sortOrder,
    layoutVersion = 0,
    refreshVersion = 0,
    filter,
    onSelectMedia,
    onCountChange,
    onQuickPlayStatus,
    initialScrollTop = 0,
    onScrollPositionChange,
    mutation = null,
}) => {
    const filterType = filter?.type || '';
    const filterValue = filter?.value || '';
    const deferredKeyword = useDeferredValue(keyword);
    const baseCacheKey = getMediaListBaseCacheKey(libraryId, sortField, sortOrder, filterType, filterValue);
    const activeKeyword = deferredKeyword.trim();
    const activeCacheKey = activeKeyword
        ? getMediaListCacheKey(libraryId, activeKeyword, sortField, sortOrder, filterType, filterValue)
        : baseCacheKey;
    const initialCache = getCachedMediaListEntry(activeCacheKey);
    const initialSearchableItems = createSearchableMediaItems(initialCache?.items || []);
    const containerRef = useRef<HTMLDivElement>(null);
    const latestScrollTopRef = useRef(0);
    const pendingRestoreRef = useRef<number | null>(initialScrollTop);
    const requestTokenRef = useRef(0);
    const scrollFrameRef = useRef<number | null>(null);
    const scrollNotifyTimerRef = useRef<number | null>(null);
    const onScrollPositionChangeRef = useRef(onScrollPositionChange);
    const [layout, setLayout] = useState({ columns: 4, gap: 20, justify: 'start' });
    const [viewportHeight, setViewportHeight] = useState(0);
    const [virtualScrollTop, setVirtualScrollTop] = useState(initialScrollTop);
    const [baseItems, setBaseItems] = useState<SearchableMediaItem[]>(() => initialSearchableItems);
    const [mediaItems, setMediaItems] = useState<any[]>(() => filterSearchableMediaItems(initialSearchableItems, keyword));
    const [isLoading, setIsLoading] = useState(() => !initialCache);

    useEffect(() => {
        onScrollPositionChangeRef.current = onScrollPositionChange;
    }, [onScrollPositionChange]);

    useEffect(() => () => {
        if (scrollFrameRef.current !== null) {
            window.cancelAnimationFrame(scrollFrameRef.current);
        }
        if (scrollNotifyTimerRef.current !== null) {
            window.clearTimeout(scrollNotifyTimerRef.current);
        }
        onScrollPositionChangeRef.current?.(latestScrollTopRef.current);
    }, []);

    const updateLayout = () => {
        if (!containerRef.current) {
            return;
        }

        const containerWidth = containerRef.current.clientWidth - MEDIA_GRID_HORIZONTAL_PADDING;
        let cols = Math.floor((containerWidth + MEDIA_GRID_MIN_GAP) / (MEDIA_CARD_WIDTH + MEDIA_GRID_MIN_GAP));
        cols = Math.max(1, cols);

        const currentGap = cols > 1 ? (containerWidth - cols * MEDIA_CARD_WIDTH) / (cols - 1) : 0;
        const gap = Math.min(Math.max(currentGap, MEDIA_GRID_MIN_GAP), MEDIA_GRID_MAX_GAP);
        const justify = cols > 1 ? 'space-between' : 'start';

        setLayout((prev) => (
            prev.columns === cols && prev.gap === gap && prev.justify === justify
                ? prev
                : { columns: cols, gap, justify }
        ));

        setViewportHeight((prev) => {
            const nextViewportHeight = containerRef.current?.clientHeight || 0;
            return prev === nextViewportHeight ? prev : nextViewportHeight;
        });
    };

    useEffect(() => {
        let frameId = 0;
        const scheduleLayoutUpdate = () => {
            window.cancelAnimationFrame(frameId);
            frameId = window.requestAnimationFrame(() => updateLayout());
        };

        const observer = new ResizeObserver(() => scheduleLayoutUpdate());
        if (containerRef.current) {
            observer.observe(containerRef.current);
            if (containerRef.current.parentElement) {
                observer.observe(containerRef.current.parentElement);
            }
        }

        window.addEventListener('resize', scheduleLayoutUpdate);
        scheduleLayoutUpdate();

        return () => {
            observer.disconnect();
            window.removeEventListener('resize', scheduleLayoutUpdate);
            window.cancelAnimationFrame(frameId);
        };
    }, []);

    useLayoutEffect(() => {
        let frameId = 0;
        frameId = window.requestAnimationFrame(() => updateLayout());
        return () => {
            window.cancelAnimationFrame(frameId);
        };
    }, [layoutVersion]);

    useLayoutEffect(() => {
        pendingRestoreRef.current = initialScrollTop;
        latestScrollTopRef.current = initialScrollTop;
        setVirtualScrollTop(initialScrollTop);

        if (containerRef.current) {
            containerRef.current.scrollTop = Math.max(0, initialScrollTop);
        }
    }, [initialScrollTop, libraryId, keyword, sortField, sortOrder, filterType, filterValue]);

    useLayoutEffect(() => {
        if (isLoading || pendingRestoreRef.current === null || !containerRef.current) {
            return;
        }

        const targetScrollTop = pendingRestoreRef.current;
        let frameId = 0;
        let nestedFrameId = 0;

        frameId = window.requestAnimationFrame(() => {
            nestedFrameId = window.requestAnimationFrame(() => {
                if (!containerRef.current) {
                    return;
                }

                const maxScrollTop = Math.max(containerRef.current.scrollHeight - containerRef.current.clientHeight, 0);
                const restoredScrollTop = Math.min(targetScrollTop, maxScrollTop);
                containerRef.current.scrollTop = restoredScrollTop;
                latestScrollTopRef.current = restoredScrollTop;
                setVirtualScrollTop(restoredScrollTop);
                pendingRestoreRef.current = null;
                onScrollPositionChange?.(restoredScrollTop);
            });
        });

        return () => {
            window.cancelAnimationFrame(frameId);
            window.cancelAnimationFrame(nestedFrameId);
        };
    }, [
        filterType,
        filterValue,
        initialScrollTop,
        isLoading,
        keyword,
        layout.columns,
        layout.gap,
        libraryId,
        mediaItems.length,
        onScrollPositionChange,
        sortField,
        sortOrder,
    ]);

    useEffect(() => {
        const cachedEntry = getCachedMediaListEntry(activeCacheKey);
        const requestToken = requestTokenRef.current + 1;
        requestTokenRef.current = requestToken;

        if (cachedEntry) {
            setBaseItems(createSearchableMediaItems(cachedEntry.items));
            setIsLoading(false);
        } else {
            setBaseItems([]);
            setIsLoading(true);
        }

        void GetMediaList(libraryId, 1, 0, sortField, sortOrder, activeKeyword, filterType, filterValue)
            .then((res: any) => {
                if (requestTokenRef.current !== requestToken) {
                    return;
                }

                const nextItems = Array.isArray(res?.items) ? res.items : [];
                const nextTotal = Number(res?.total || 0);
                persistMediaListCache(activeCacheKey, {
                    items: nextItems,
                    total: nextTotal,
                });
                setBaseItems(createSearchableMediaItems(nextItems));
                setIsLoading(false);
            })
            .catch((err) => {
                console.error(err);
                if (requestTokenRef.current === requestToken && !cachedEntry) {
                    setIsLoading(false);
                }
            });

        return () => {
            requestTokenRef.current += 1;
        };
    }, [activeCacheKey, activeKeyword, filterType, filterValue, libraryId, refreshVersion, sortField, sortOrder]);

    useEffect(() => {
        const nextItems = filterSearchableMediaItems(baseItems, deferredKeyword);
        startTransition(() => {
            setMediaItems(nextItems);
        });
        onCountChange?.(nextItems.length);
    }, [baseItems, deferredKeyword, onCountChange]);

    useEffect(() => {
        if (!mutation) {
            return;
        }

        if (mutation.type === 'merge') {
            const normalizedMediaID = typeof mutation.media?.id === 'string' ? mutation.media.id.trim() : '';
            if (!normalizedMediaID) {
                return;
            }

            mergeMediaIntoCachedMediaLists(mutation.media);
            setBaseItems((prev) => {
                let changed = false;
                const nextItems = prev.map((item) => {
                    if (item.media?.id !== normalizedMediaID) {
                        return item;
                    }

                    changed = true;
                    return mergeSearchableMediaItem(item, mutation.media);
                });
                return changed ? nextItems : prev;
            });
            return;
        }

        const normalizedMediaID = typeof mutation.mediaId === 'string' ? mutation.mediaId.trim() : '';
        if (!normalizedMediaID) {
            return;
        }

        removeMediaFromCachedMediaLists(normalizedMediaID);
        setBaseItems((prev) => {
            const nextItems = prev.filter((item) => item.media?.id !== normalizedMediaID);
            return nextItems.length === prev.length ? prev : nextItems;
        });
    }, [mutation]);

    const handleSelectMedia = (media: any) => {
        seedMediaDetailCache(media);
        onScrollPositionChange?.(latestScrollTopRef.current);
        onSelectMedia(media);
    };

    const scheduleScrollUpdate = () => {
        if (scrollFrameRef.current !== null) {
            return;
        }

        scrollFrameRef.current = window.requestAnimationFrame(() => {
            scrollFrameRef.current = null;
            setVirtualScrollTop(latestScrollTopRef.current);
        });
    };

    const scheduleScrollPositionNotify = () => {
        if (scrollNotifyTimerRef.current !== null) {
            window.clearTimeout(scrollNotifyTimerRef.current);
        }

        scrollNotifyTimerRef.current = window.setTimeout(() => {
            scrollNotifyTimerRef.current = null;
            onScrollPositionChangeRef.current?.(latestScrollTopRef.current);
        }, 120);
    };

    const totalRows = Math.ceil(mediaItems.length / layout.columns);
    const rowHeight = MEDIA_CARD_HEIGHT + MEDIA_GRID_ROW_GAP;
    const effectiveViewportHeight = viewportHeight > 0 ? viewportHeight : rowHeight;
    const startRow = Math.max(0, Math.floor(virtualScrollTop / rowHeight) - VIRTUAL_OVERSCAN_ROWS);
    const endRow = Math.min(
        totalRows,
        Math.ceil((virtualScrollTop + effectiveViewportHeight) / rowHeight) + VIRTUAL_OVERSCAN_ROWS,
    );
    const startIndex = startRow * layout.columns;
    const endIndex = Math.min(mediaItems.length, endRow * layout.columns);
    const visibleItems = mediaItems.slice(startIndex, endIndex);
    const topOffset = startRow * rowHeight;
    const totalContentHeight = totalRows === 0
        ? 0
        : totalRows * MEDIA_CARD_HEIGHT + Math.max(0, totalRows - 1) * MEDIA_GRID_ROW_GAP;

    return (
        <div
            ref={containerRef}
            className="grid-container"
            onScroll={(event) => {
                const nextScrollTop = event.currentTarget.scrollTop;
                latestScrollTopRef.current = nextScrollTop;
                scheduleScrollUpdate();
                scheduleScrollPositionNotify();
            }}
        >
            {!isLoading && mediaItems.length === 0 && (
                <div className="grid-feedback">
                    {'\u6ca1\u6709\u627e\u5230\u7b26\u5408\u6761\u4ef6\u7684\u5a92\u4f53\u5185\u5bb9'}
                </div>
            )}

            {isLoading && baseItems.length === 0 && (
                <div className="grid-feedback loading">
                    {'\u6b63\u5728\u52a0\u8f7d\u5a92\u4f53\u5185\u5bb9...'}
                </div>
            )}

            {mediaItems.length > 0 && (
                <div
                    className="grid-virtual-spacer"
                    style={{ height: `${Math.max(totalContentHeight, effectiveViewportHeight)}px` }}
                >
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
                        {visibleItems.map((item) => (
                            <MediaCard
                                key={item.id}
                                media={item}
                                onClick={() => handleSelectMedia(item)}
                                onQuickPlayStatus={onQuickPlayStatus}
                                onPrefetch={() => {
                                    seedMediaDetailCache(item);
                                    prefetchMediaDetailCacheEntry(item.id);
                                }}
                            />
                        ))}
                    </div>
                </div>
            )}
        </div>
    );
};

export default MediaGrid;
