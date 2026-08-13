import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from 'react';
import { Info, Play, TriangleAlert } from 'lucide-react';
import './App.css';
import './library-refine.css';
import './navi-redesign.css';
import {
    GetActorStats,
    GetDesktopSettings,
    GetGenreStats,
    GetLibraries,
    PlayRandomLibraryMedia,
    ScanLibraryWithMode,
} from "../wailsjs/go/main/App";
import { EventsOn, WindowSetDarkTheme, WindowSetTitle } from "../wailsjs/runtime/runtime";
import Sidebar from './components/Sidebar';
import TopBar from './components/TopBar';
import MediaGrid, { getMediaListCacheKey, type MediaGridMutation } from './components/MediaGrid';
import CategoryGrid, {
    type CategorySortField,
    type CategoryStats,
    type CategoryViewMode,
} from './components/CategoryGrid';
import SettingsPage from './components/SettingsPage';
import LibraryModal from './components/LibraryModal';
import LibraryEditModal from './components/LibraryEditModal';
import MediaDetail from './components/MediaDetail';
import {
    loadInitialLibraryState,
    mergeMediaIntoCachedMediaLists,
    persistCurrentLibraryID,
    persistLibraries,
} from './utils/persistentCache';
import { mergeMediaDetailCacheEntry, mergeMediaStateCacheEntry, seedMediaDetailCache } from './utils/mediaDetailCache';
import {
    applyMediaStateUpdate,
    getChangedMediaStateFields,
    normalizeMediaStateEvent,
    type MediaStateUpdate,
} from './utils/mediaPlaybackState';
import { markComponentRender } from './utils/performanceDiagnostics';
import type { StatusKind } from './types/status';
import { putBoundedScrollState } from './utils/listViewState';
import { getTopBarBackLabel } from './utils/filterNavigation';
import { formatLastScanLabel, formatLibrarySize } from './utils/library';
import { shouldReplaceActorFilterOnSearchChange } from './utils/mediaSearch';
import {
    loadSortPreferences,
    persistSortPreferences,
    type SortConfig,
    type SortField,
    type SortOrder,
    type SortViewName,
} from './utils/sortPreferences';
import ScanTaskPanel from './components/ScanTaskPanel';
import { scanProgressStore } from './utils/scanProgressStore';
import {
	activateScanTaskFromEvent,
	activateScanTaskFromResponse,
	beginScanRequest,
	clearScanTaskLifecycle,
	completeScanTask,
	createScanTaskLifecycle,
	isCurrentScanEvent,
    registerScanEventListeners,
    scanEventIdentity,
} from './utils/scanTaskEvents';

type ViewName = 'libs' | 'settings' | 'actor' | 'genre' | 'watched' | 'favorite';
type StatusAction = { label: string; onClick: () => void };
type StatusToast = { text: string; kind: StatusKind; action?: StatusAction } | null;
type SortOption = { field: SortField; label: string };
type FilterState = { type: string; value: string; label: string; showHeaderLabel?: boolean } | null;
type FilterReturnContext = {
    view: ViewName;
    media: any | null;
    searchKeyword: string;
    filter: FilterState;
} | null;
const APP_TITLE = 'Navi';

// 刮削事件是成串来的：先攒 1.2s，且两次真正的刷新之间至少隔 2s。
const METADATA_REFRESH_DEBOUNCE_MS = 1200;
const METADATA_REFRESH_MIN_INTERVAL_MS = 2000;

const VIEW_LABELS: Record<Exclude<ViewName, 'libs'>, string> = {
    settings: '设置',
    actor: '演员',
    genre: '类别',
    watched: '已看',
    favorite: '收藏',
};

const FILTER_KIND_LABELS: Record<string, string> = {
    actor: '演员',
    genre: '类别',
    series: '系列',
    watched: '已看',
    favorite: '收藏',
};

const EMPTY_CATEGORY_STATS: CategoryStats = { total: 0, withImage: 0, works: 0 };

// 演员/类别页的排序维度与媒体列表不同，所以不复用 SortField。
const CATEGORY_SORT_OPTIONS: Array<{ field: string; label: string }> = [
    { field: 'count', label: '作品数' },
    { field: 'name', label: '名称' },
];

const SEARCH_INPUT_VIEWS = new Set<ViewName>(['libs', 'watched', 'favorite', 'actor', 'genre']);
const MEDIA_ACTION_VIEWS = new Set<ViewName>(['libs', 'watched', 'favorite']);

const LIBRARY_SORT_OPTIONS: SortOption[] = [
    { field: 'created_at', label: '加入日期' },
    { field: 'release_date', label: '发行日期' },
    { field: 'video_codec', label: '视频编码' },
    { field: 'last_watched', label: '观看时间' },
];

const WATCHED_SORT_OPTIONS: SortOption[] = [
    { field: 'last_watched', label: '观看时间' },
    { field: 'created_at', label: '加入日期' },
    { field: 'rating', label: '评分' },
];

const FAVORITE_SORT_OPTIONS: SortOption[] = [
    { field: 'favorite_at', label: '收藏时间' },
    { field: 'created_at', label: '加入日期' },
    { field: 'rating', label: '评分' },
];

const formatError = (error: unknown) => {
    if (error instanceof Error && error.message) {
        return error.message;
    }
    if (typeof error === 'string') {
        return error;
    }
    return '未知错误';
};

const getMediaGridScrollKey = (
    libraryId: string,
    view: ViewName,
    keyword: string,
    sortField: SortField,
    sortOrder: SortOrder,
    filter: FilterState,
) => {
    const normalizedLibraryID = typeof libraryId === 'string' ? libraryId.trim() : '';
    if (!normalizedLibraryID) {
        return '';
    }

    if (view === 'watched') {
        return getMediaListCacheKey(normalizedLibraryID, keyword, sortField, sortOrder, 'watched', 'true');
    }

    if (view === 'favorite') {
        return getMediaListCacheKey(normalizedLibraryID, keyword, sortField, sortOrder, 'favorite', 'true');
    }

    if (view !== 'libs') {
        return '';
    }

    return getMediaListCacheKey(
        normalizedLibraryID,
        keyword,
        sortField,
        sortOrder,
        filter?.type || '',
        filter?.value || '',
    );
};

function App() {
    markComponentRender('App');
    const initialLibraryStateRef = useRef<ReturnType<typeof loadInitialLibraryState> | null>(null);
    if (!initialLibraryStateRef.current) {
        initialLibraryStateRef.current = loadInitialLibraryState();
    }
    const initialLibraryState = initialLibraryStateRef.current;
    const [layoutVersion, setLayoutVersion] = useState(0);
    const [contentRefreshVersion, setContentRefreshVersion] = useState(0);
    const [view, setView] = useState<ViewName>('libs');
    const [libraries, setLibraries] = useState<any[]>(() => initialLibraryState.libraries);
    const [currentLib, setCurrentLib] = useState<any>(() => initialLibraryState.currentLibrary);
    const [scanRequestPendingLibraryID, setScanRequestPendingLibraryID] = useState('');
    const activeScan = useSyncExternalStore(
        scanProgressStore.subscribe,
        () => scanProgressStore.getSnapshot(currentLib?.id || ''),
        () => null,
    );
    const [searchKeyword, setSearchKeyword] = useState('');
    const [debouncedMediaSearch, setDebouncedMediaSearch] = useState('');
    const [showLibModal, setShowLibModal] = useState(false);
    const [editingLib, setEditingLib] = useState<any>(null);
    const [selectedMedia, setSelectedMedia] = useState<any>(null);
    const [statusToast, setStatusToast] = useState<StatusToast>(null);
    const [mediaCount, setMediaCount] = useState(() => {
        const initialCount = initialLibraryState.currentLibrary?.media_count;
        return typeof initialCount === 'number' ? initialCount : 0;
    });
    const [activeFilter, setActiveFilter] = useState<FilterState>(null);
    const [filterReturnContext, setFilterReturnContext] = useState<FilterReturnContext>(null);
    const [categoryStats, setCategoryStats] = useState<CategoryStats>(EMPTY_CATEGORY_STATS);
    const [categorySort, setCategorySort] = useState<{ field: CategorySortField; order: SortOrder }>(
        { field: 'count', order: 'desc' },
    );
    const [categoryViewMode, setCategoryViewMode] = useState<CategoryViewMode>('grid');
    const [sortStateByView, setSortStateByView] = useState<Record<SortViewName, SortConfig>>(() => loadSortPreferences());
    const [gridScrollTops, setGridScrollTops] = useState<Record<string, number>>({});
    const [listMutation, setListMutation] = useState<MediaGridMutation | null>(null);
    const [latestMediaStateUpdate, setLatestMediaStateUpdate] = useState<MediaStateUpdate | null>(null);
    const scanStartedAtRef = useRef<number | null>(null);
    const scanModeRef = useRef<string>('');
    const scanRequestPendingRef = useRef(new Set<string>());
    const scanTaskLifecycleRef = useRef(createScanTaskLifecycle());
    const resetTitleTimerRef = useRef<number | null>(null);
    const metadataRefreshTimerRef = useRef<number | null>(null);
    const lastContentRefreshAtRef = useRef(0);
    const statusTimerRef = useRef<number | null>(null);
    const currentLibRef = useRef<any>(null);
    const mediaStateByIDRef = useRef(new Map<string, MediaStateUpdate>());

    const setAppTitle = (title: string) => {
        WindowSetTitle(title);
    };

    const currentSortView: SortViewName = view === 'watched' || view === 'favorite' ? view : 'libs';
    const { field: sortField, order: sortOrder } = sortStateByView[currentSortView];
    const currentSortOptions = currentSortView === 'watched'
        ? WATCHED_SORT_OPTIONS
        : currentSortView === 'favorite'
            ? FAVORITE_SORT_OPTIONS
            : LIBRARY_SORT_OPTIONS;
    const mediaSearchKeyword = searchKeyword.trim() ? debouncedMediaSearch : '';
    const currentGridScrollKey = getMediaGridScrollKey(
        currentLib?.id,
        view,
        mediaSearchKeyword,
        sortField,
        sortOrder,
        activeFilter,
    );
    const currentGridScrollTop = currentGridScrollKey ? (gridScrollTops[currentGridScrollKey] || 0) : 0;

    const handleGridScrollPositionChange = useCallback((scrollTop: number) => {
        if (!currentGridScrollKey) {
            return;
        }

        const normalizedScrollTop = Number.isFinite(scrollTop) ? Math.max(0, scrollTop) : 0;
        setGridScrollTops((prev) => {
            if (prev[currentGridScrollKey] === normalizedScrollTop) {
                return prev;
            }

            return putBoundedScrollState(prev, currentGridScrollKey, normalizedScrollTop);
        });
    }, [currentGridScrollKey]);

    useEffect(() => {
        const timer = window.setTimeout(() => setDebouncedMediaSearch(searchKeyword.trim()), searchKeyword.trim() ? 250 : 0);
        return () => window.clearTimeout(timer);
    }, [searchKeyword]);

    const scheduleTitleReset = (delay = 3500) => {
        if (resetTitleTimerRef.current) {
            window.clearTimeout(resetTitleTimerRef.current);
        }
        resetTitleTimerRef.current = window.setTimeout(() => {
            setAppTitle(APP_TITLE);
            resetTitleTimerRef.current = null;
        }, delay);
    };

    const updateScanTitle = (data: any, fallbackPrefix: string) => {
        const current = typeof data?.current === 'number' ? data.current : 0;
        const total = typeof data?.total === 'number' ? data.total : 0;
        const elapsedSeconds = scanStartedAtRef.current
            ? Math.max(0, Math.floor((Date.now() - scanStartedAtRef.current) / 1000))
            : 0;
        const ratioText = total > 0 ? `${current}/${total}` : `${current}`;
        const message = typeof data?.message === 'string' ? data.message.trim() : '';
        const suffix = elapsedSeconds > 0 ? `，耗时 ${elapsedSeconds}s` : '';
        const detail = message ? ` ${message}` : '';
        setAppTitle(`${APP_TITLE} - ${fallbackPrefix}${ratioText}${detail}${suffix}`);
    };

    const showStatus = useCallback((msg: string, kind: StatusKind = 'info', action?: StatusAction) => {
        setStatusToast({ text: msg, kind, action });
        if (statusTimerRef.current !== null) {
            window.clearTimeout(statusTimerRef.current);
        }
        statusTimerRef.current = window.setTimeout(() => {
            setStatusToast(null);
            statusTimerRef.current = null;
        }, 5000);
    }, []);

    const startScanForLibrary = async (libraryId: string, mode: string) => {
        scanModeRef.current = mode;
        const generation = beginScanRequest(scanTaskLifecycleRef.current, libraryId);
        const task = await ScanLibraryWithMode(libraryId, mode);
        if (typeof task?.task_id === 'string' && task.task_id) {
            const activated = activateScanTaskFromResponse(scanTaskLifecycleRef.current, libraryId, generation, task.task_id);
            if (activated && !scanProgressStore.getSnapshot(libraryId)) {
                scanProgressStore.set({
                    taskId: task.task_id,
                    libraryId,
                    libraryName: typeof task?.library_name === 'string' ? task.library_name : '',
                    mode: typeof task?.mode === 'string' ? task.mode : mode,
                    phase: 'starting',
                    current: 0,
                    total: 0,
                    message: '正在启动扫描',
                });
            }
        }
    };

    const loadLibraries = async () => {
        try {
            const libs = await GetLibraries();
            const nextLibraries = libs || [];
            setLibraries(nextLibraries);
            setCurrentLib((prev: any) => {
                if (nextLibraries.length === 0) {
                    return null;
                }
                if (!prev) {
                    return nextLibraries[0];
                }
                return nextLibraries.find((lib: any) => lib.id === prev.id) || nextLibraries[0];
            });
            setEditingLib((prev: any) => {
                if (!prev) {
                    return prev;
                }
                return nextLibraries.find((lib: any) => lib.id === prev.id) || null;
            });
        } catch (error) {
            console.error(error);
        }
    };

    useEffect(() => {
        currentLibRef.current = currentLib;
    }, [currentLib]);

    useEffect(() => {
        persistLibraries(libraries);
    }, [libraries]);

    useEffect(() => {
        persistCurrentLibraryID(currentLib?.id || '');
    }, [currentLib]);

    useEffect(() => {
        persistSortPreferences(sortStateByView);
    }, [sortStateByView]);

    useEffect(() => {
        let frameId = 0;
        const handleResize = () => {
            window.cancelAnimationFrame(frameId);
            frameId = window.requestAnimationFrame(() => {
                setLayoutVersion((prev) => prev + 1);
            });
        };

        window.addEventListener('resize', handleResize);

        return () => {
            window.removeEventListener('resize', handleResize);
            window.cancelAnimationFrame(frameId);
        };
    }, []);

    useEffect(() => {
        setAppTitle(APP_TITLE);
        WindowSetDarkTheme();
        loadLibraries();

        GetDesktopSettings().then((settings: any) => {
            if (settings?.theme) {
                document.body.className = settings.theme;
            }
        });

        const onScanStart = (data: any) => {
            const { libraryId, taskId } = scanEventIdentity(data);
            if (!libraryId || !taskId || !activateScanTaskFromEvent(scanTaskLifecycleRef.current, data)) {
                return;
            }
            scanProgressStore.set({
                taskId,
                libraryId: typeof data?.library_id === 'string' ? data.library_id : '',
                libraryName: typeof data?.library_name === 'string' ? data.library_name : '',
                mode: typeof data?.mode === 'string' ? data.mode : (scanModeRef.current || ''),
                phase: typeof data?.phase === 'string' ? data.phase : 'started',
                current: typeof data?.current === 'number'
                    ? data.current
                    : (typeof data?.new_found === 'number' ? data.new_found : 0),
                total: typeof data?.total === 'number' ? data.total : 0,
                message: typeof data?.message === 'string' ? data.message : '',
            });
            if (currentLibRef.current?.id !== libraryId) {
                return;
            }
            scanStartedAtRef.current = Date.now();
            updateScanTitle(data, '扫描 ');
        };

        const onScanProgress = (data: any) => {
            const { libraryId } = scanEventIdentity(data);
            if (!isCurrentScanEvent(scanTaskLifecycleRef.current.activeTaskIDs.get(libraryId), data)) {
                return;
            }
            scanProgressStore.update(libraryId, (prev) => ({
                taskId: typeof data?.task_id === 'string' ? data.task_id : (prev?.taskId || ''),
                libraryId: typeof data?.library_id === 'string' ? data.library_id : (prev?.libraryId || ''),
                libraryName: typeof data?.library_name === 'string' ? data.library_name : (prev?.libraryName || ''),
                mode: typeof data?.mode === 'string' ? data.mode : (prev?.mode || scanModeRef.current || ''),
                phase: typeof data?.phase === 'string' ? data.phase : (prev?.phase || 'progress'),
                current: typeof data?.current === 'number'
                    ? data.current
                    : (typeof data?.new_found === 'number' ? data.new_found : (prev?.current || 0)),
                total: typeof data?.total === 'number' ? data.total : (prev?.total || 0),
                message: typeof data?.message === 'string' ? data.message : (prev?.message || ''),
            }));
            if (currentLibRef.current?.id !== libraryId) {
                return;
            }
            updateScanTitle(data, '扫描 ');
        };

        const acceptTerminal = (data: any, fallbackPhase: string): boolean => {
			const { libraryId, taskId } = scanEventIdentity(data);
			if (!libraryId || !taskId || !completeScanTask(scanTaskLifecycleRef.current, data)) {
				return false;
			}
			const previous = scanProgressStore.getLibraryState(libraryId).active;
			scanProgressStore.complete({
				taskId,
				libraryId,
				libraryName: typeof data?.library_name === 'string' ? data.library_name : (previous?.libraryName || ''),
				mode: typeof data?.mode === 'string' ? data.mode : (previous?.mode || ''),
				phase: typeof data?.phase === 'string' ? data.phase : fallbackPhase,
				current: typeof data?.current === 'number' ? data.current : (previous?.current || 0),
				total: typeof data?.total === 'number' ? data.total : (previous?.total || 0),
				message: typeof data?.message === 'string' ? data.message : (previous?.message || ''),
			});
			return true;
        };

        const onScanComplete = (data: any) => {
            if (!acceptTerminal(data, 'completed')) {
                return;
            }
            loadLibraries();
            if (currentLibRef.current?.id !== scanEventIdentity(data).libraryId) {
                return;
            }
            showStatus(`扫描完成：${data?.library_name || ''}`);
            updateScanTitle(data, '完成 ');
            scanStartedAtRef.current = null;
            scanModeRef.current = '';
            scheduleTitleReset();
            lastContentRefreshAtRef.current = Date.now();
            setContentRefreshVersion((prev) => prev + 1);
        };

        const unsubMetadata = EventsOn("media:metadata-updated", (data: any) => {
            const activeLibraryId = currentLibRef.current?.id;
            const eventLibraryId = typeof data?.library_id === 'string' ? data.library_id : '';
            if (activeLibraryId && eventLibraryId && activeLibraryId !== eventLibraryId) {
                return;
            }

            // 扫描 / 刮削时这个事件是连着来的，每刷一次整张网格就重排一次。
            // 扫描期间干脆不刷：scan:completed 收尾时会补一次整表刷新；
            // 详情页那条媒体走 media:state-updated 单条更新，不需要整列刷。
            if (activeLibraryId && scanProgressStore.getSnapshot(activeLibraryId)) {
                return;
            }

            if (metadataRefreshTimerRef.current !== null) {
                window.clearTimeout(metadataRefreshTimerRef.current);
            }
            const sinceLastRefresh = Date.now() - lastContentRefreshAtRef.current;
            const delay = Math.max(
                METADATA_REFRESH_DEBOUNCE_MS,
                METADATA_REFRESH_MIN_INTERVAL_MS - sinceLastRefresh,
            );
            metadataRefreshTimerRef.current = window.setTimeout(() => {
                lastContentRefreshAtRef.current = Date.now();
                setContentRefreshVersion((prev) => prev + 1);
                metadataRefreshTimerRef.current = null;
            }, delay);
        });

        const unsubMediaState = EventsOn("media:state-updated", (data: any) => {
            const update = normalizeMediaStateEvent(data);
            if (!update) {
                return;
            }

            const previous = mediaStateByIDRef.current.get(update.id);
            const current = previous || { id: update.id };
            const next = applyMediaStateUpdate(current, update);
            if (next === current) {
                return;
            }
            const changedFields = getChangedMediaStateFields(previous, next);
            mediaStateByIDRef.current.delete(update.id);
            mediaStateByIDRef.current.set(update.id, next);
            while (mediaStateByIDRef.current.size > 256) {
                const oldestMediaID = mediaStateByIDRef.current.keys().next().value;
                if (!oldestMediaID) {
                    break;
                }
                mediaStateByIDRef.current.delete(oldestMediaID);
            }

            mergeMediaStateCacheEntry(update);
            mergeMediaIntoCachedMediaLists(update, changedFields);
            setListMutation({ type: 'merge', media: update, changedFields });
            setSelectedMedia((prev: any) => (prev?.id === update.id ? applyMediaStateUpdate(prev, update) : prev));
            setLatestMediaStateUpdate(update);
        });

		const unsubPlayerSyncWarning = EventsOn("player:sync-warning", (data: any) => {
			const message = typeof data?.message === 'string' ? data.message.trim() : '';
			showStatus(message || 'PotPlayer 播放已启动，但进度同步不可用', 'error', {
				label: '去设置',
				onClick: () => handleOpenSettings(),
			});
		});

        const onScanFail = (data: any) => {
            if (!acceptTerminal(data, 'failed')) {
                return;
            }
            if (currentLibRef.current?.id !== scanEventIdentity(data).libraryId) {
                return;
            }
            showStatus(`扫描失败：${data?.message || '未知错误'}`, 'error');
            updateScanTitle(data, '失败 ');
            scanStartedAtRef.current = null;
            scanModeRef.current = '';
            scheduleTitleReset();
        };

        const onScanIncomplete = (data: any) => {
            if (!acceptTerminal(data, 'incomplete')) {
                return;
            }
            if (currentLibRef.current?.id !== scanEventIdentity(data).libraryId) {
                return;
            }
            showStatus(`扫描未完成：${data?.message || '目录无法完整访问'}`, 'error');
            updateScanTitle(data, '未完成 ');
            scanStartedAtRef.current = null;
            scanModeRef.current = '';
            scheduleTitleReset();
        };

        const onScanCanceled = (data: any) => {
            if (!acceptTerminal(data, 'canceled')) {
                return;
            }
            if (currentLibRef.current?.id !== scanEventIdentity(data).libraryId) {
                return;
            }
            showStatus('扫描已取消');
            updateScanTitle(data, '已取消 ');
            scanStartedAtRef.current = null;
            scanModeRef.current = '';
            scheduleTitleReset();
        };

        const unsubscribeScanEvents = registerScanEventListeners(
            (event, handler) => EventsOn(event, handler),
            {
                'scan:start': onScanStart,
                'scan:progress': onScanProgress,
                'scan:completed': onScanComplete,
                'scan:failed': onScanFail,
                'scan:incomplete': onScanIncomplete,
                'scan:canceled': onScanCanceled,
            },
        );

        return () => {
            unsubscribeScanEvents();
            unsubMetadata();
            unsubMediaState();
			unsubPlayerSyncWarning();
            if (resetTitleTimerRef.current) {
                window.clearTimeout(resetTitleTimerRef.current);
            }
            if (metadataRefreshTimerRef.current !== null) {
                window.clearTimeout(metadataRefreshTimerRef.current);
            }
            if (statusTimerRef.current !== null) {
                window.clearTimeout(statusTimerRef.current);
            }
            scanModeRef.current = '';
            scanRequestPendingRef.current.clear();
            scanProgressStore.clear();
            clearScanTaskLifecycle(scanTaskLifecycleRef.current);
            setAppTitle(APP_TITLE);
        };
    }, []);

    const handleSearchChange = (keyword: string) => {
        if (shouldReplaceActorFilterOnSearchChange(activeFilter?.type, searchKeyword, keyword)) {
            setActiveFilter(null);
            setFilterReturnContext(null);
            setSelectedMedia(null);
            setView('libs');
        }
        setSearchKeyword(keyword);
    };

    const clearFilter = () => {
        const hadActiveFilter = Boolean(activeFilter);
        setActiveFilter(null);
        setSearchKeyword('');
        setFilterReturnContext(null);
        setSelectedMedia(null);
        if (hadActiveFilter) {
            setView('libs');
        }
    };

    const returnToFilterSource = () => {
        if (!filterReturnContext) {
            return;
        }
        setActiveFilter(filterReturnContext.filter);
        setSearchKeyword(filterReturnContext.searchKeyword);
        setView(filterReturnContext.view);
        setSelectedMedia(filterReturnContext.media);
        setFilterReturnContext(null);
    };

    const applyFilter = (
        filter: { type: string; value: string; label: string },
        returnContext?: FilterReturnContext,
        showHeaderLabel = true,
    ) => {
        setActiveFilter({ ...filter, showHeaderLabel });
        setSearchKeyword(filter.label);
        setFilterReturnContext(returnContext || null);
        setSelectedMedia(null);
        setView('libs');
    };

    const applyFilterFromView = (sourceView: ViewName, filter: { type: string; value: string; label: string }) => {
        applyFilter(filter, {
            view: sourceView,
            media: null,
            searchKeyword,
            filter: activeFilter,
        });
    };

    const applyFilterFromDetail = (filter: { type: string; value: string; label: string }) => {
        applyFilter(filter, {
            view,
            media: selectedMedia,
            searchKeyword,
            filter: activeFilter,
        }, false);
    };

    const handleSelectMedia = useCallback((media: any) => {
        seedMediaDetailCache(media);
        setSelectedMedia(media);
    }, []);

    const handleDetailMediaChange = useCallback((media: any) => {
        seedMediaDetailCache(media);
        setListMutation({ type: 'merge', media });
        setSelectedMedia((prev: any) => {
            if (!prev || prev.id !== media.id) {
                return media;
            }
            return { ...prev, ...media };
        });
    }, []);

    // 网格卡片上的收藏等操作只应更新列表，不能像详情页那样把媒体设为当前选中项。
    const handleListMediaChange = useCallback((media: any) => {
        seedMediaDetailCache(media);
        setListMutation({ type: 'merge', media });
        setSelectedMedia((prev: any) => (prev && prev.id === media.id ? { ...prev, ...media } : prev));
    }, []);

    const handleDetailDelete = useCallback((mediaID: string) => {
        setListMutation({ type: 'remove', mediaId: mediaID });
        setSelectedMedia((prev: any) => (prev?.id === mediaID ? null : prev));
    }, []);

    const resetWorkspaceState = () => {
        setSelectedMedia(null);
        setActiveFilter(null);
        setFilterReturnContext(null);
        setSearchKeyword('');
    };

    const handleSelectLibrary = (lib: any) => {
        setCurrentLib(lib);
        setView('libs');
        resetWorkspaceState();
    };

    const handleOpenSettings = () => {
        resetWorkspaceState();
        setView('settings');
    };

    const handleSelectView = (nextView: ViewName) => {
        resetWorkspaceState();
        setView(nextView);
    };

    const handleNavigateHome = () => {
        resetWorkspaceState();
        setView('libs');
    };

    const handleLibCreated = async (createdLib: any) => {
        setShowLibModal(false);
        resetWorkspaceState();
        setView('libs');
        if (createdLib) {
            setCurrentLib(createdLib);
        }
        loadLibraries();

        if (createdLib?.id) {
            try {
                await startScanForLibrary(createdLib.id, 'incremental');
            } catch (error) {
                showStatus(`新建媒体库成功，但自动扫描失败：${formatError(error)}`, 'error');
            }
            return;
        }

        showStatus('\u65b0\u5efa\u5a92\u4f53\u5e93\u6210\u529f');
    };
    const handleLibSaved = () => {
        setEditingLib(null);
        loadLibraries();
        showStatus('\u5a92\u4f53\u5e93\u5df2\u4fdd\u5b58');
    };

    const handleLibDeleted = () => {
        setEditingLib(null);
        setCurrentLib(null);
        loadLibraries();
        showStatus('\u5a92\u4f53\u5e93\u5df2\u5220\u9664');
    };

    const handleScanWithMode = async (mode: string) => {
        if (!currentLib) {
            return;
        }
        const libraryId = currentLib.id;
        if (activeScan || scanRequestPendingRef.current.has(libraryId)) {
            return;
        }
        scanRequestPendingRef.current.add(libraryId);
        setScanRequestPendingLibraryID(libraryId);
        try {
            await startScanForLibrary(libraryId, mode);
        } catch (error) {
            showStatus(`扫描启动失败：${formatError(error)}`, 'error');
        } finally {
            scanRequestPendingRef.current.delete(libraryId);
            setScanRequestPendingLibraryID((current) => current === libraryId ? '' : current);
        }
    };
    const handleRandomPlay = async () => {
        if (!currentLib) {
            return;
        }
        try {
            const filename = await PlayRandomLibraryMedia(currentLib.id);
            showStatus(`随机播放：${filename}`, 'play');
        } catch (error) {
            showStatus(`随机播放失败：${formatError(error)}`, 'error');
        }
    };

    const handleSortSelect = (field: string) => {
        const nextField = field as SortField;
        setSortStateByView((prev) => {
            const currentSort = prev[currentSortView];
            if (nextField === currentSort.field) {
                return {
                    ...prev,
                    [currentSortView]: {
                        field: currentSort.field,
                        order: currentSort.order === 'desc' ? 'asc' : 'desc',
                    },
                };
            }

            return {
                ...prev,
                [currentSortView]: {
                    field: nextField,
                    order: 'desc',
                },
            };
        });
    };

    // 演员/类别页有自己的排序（作品数 / 名称），与媒体列表的排序互不影响。
    const handleCategorySortSelect = (field: string) => {
        const nextField = field as CategorySortField;
        setCategorySort((prev) => (
            prev.field === nextField
                ? { field: prev.field, order: prev.order === 'desc' ? 'asc' : 'desc' }
                : { field: nextField, order: 'desc' }
        ));
    };

    const handleCategoryStatsChange = useCallback((stats: CategoryStats) => {
        setCategoryStats((prev) => (
            prev.total === stats.total && prev.withImage === stats.withImage && prev.works === stats.works
                ? prev
                : stats
        ));
    }, []);

    const currentLibraryName = currentLib?.name || '未选择媒体库';
    const baseCount = typeof currentLib?.media_count === 'number' ? currentLib.media_count : mediaCount;
    const headerCount = (view === 'libs' || view === 'watched' || view === 'favorite') ? mediaCount : baseCount;
    const showFilterInHeading = Boolean(activeFilter?.label) && activeFilter?.showHeaderLabel !== false;
    const headerTitle = view === 'settings'
        ? VIEW_LABELS.settings
        : showFilterInHeading
            ? (activeFilter?.label || '')
            : view === 'libs'
                ? currentLibraryName
                : VIEW_LABELS[view];
    // 媒体库概览：部数 · 容量 · 上次扫描；筛选态下只说明当前结果条数。
    const libraryOverview = [
        `${(headerCount || 0).toLocaleString()} 部`,
        formatLibrarySize(currentLib?.total_size),
        formatLastScanLabel(currentLib?.last_scan),
    ].filter(Boolean).join(' · ');
    const headerStats = view === 'actor'
        ? `${categoryStats.total} 人 · ${categoryStats.withImage} 人有头像 · 覆盖 ${categoryStats.works.toLocaleString()} 部`
        : view === 'genre'
            ? `${categoryStats.total} 个类别`
            : view === 'settings'
                ? 'Navi 1.4.0 · 配置已同步'
                : (view === 'libs' && !activeFilter && !searchKeyword.trim())
                    ? libraryOverview
                    : `${(headerCount || 0).toLocaleString()} 部`;
    const topBarBackLabel = view === 'settings' ? '返回媒体库' : getTopBarBackLabel(filterReturnContext, view);
    const topBarBackAction = view === 'settings'
        ? handleNavigateHome
        : filterReturnContext
        ? returnToFilterSource
        : topBarBackLabel
            ? handleNavigateHome
            : undefined;
    const searchEnabled = Boolean(currentLib && SEARCH_INPUT_VIEWS.has(view));
    // 已看 / 收藏 一条都没有时，搜索框没有可搜的目标，整体压暗并禁用。
    const isEmptyListView = (view === 'watched' || view === 'favorite')
        && mediaCount === 0
        && !searchKeyword.trim();
    const showLibraryActions = Boolean(currentLib && view === 'libs');
    const showListActions = Boolean(currentLib && MEDIA_ACTION_VIEWS.has(view));
    const showCategorySort = Boolean(currentLib && (view === 'actor' || view === 'genre'));
    const searchPlaceholder = view === 'actor'
        ? '\u641c\u7d22\u6f14\u5458'
        : view === 'genre'
            ? '\u641c\u7d22\u6807\u7b7e'
            : '\u641c\u7d22\u5a92\u4f53\u3001\u6f14\u5458\u3001\u6807\u7b7e';
    const isDetailOpen = Boolean(selectedMedia);

    const renderWorkspaceContent = () => {
        if (!currentLib && view !== 'settings') {
            return (
                <div className="workspace-empty-state">
                    <div className="workspace-empty-state-inner">
                        <span className="workspace-empty-eyebrow">媒体库</span>
                        <h2>还没有可用的媒体库</h2>
                        <p>先创建一个媒体库，然后即可开始扫描、搜索和浏览你的内容。</p>
                        <button type="button" className="workspace-empty-action" onClick={() => setShowLibModal(true)}>
                            新建媒体库
                        </button>
                    </div>
                </div>
            );
        }

        switch (view) {
            case 'watched':
                return (
                    <MediaGrid
                        libraryId={currentLib.id}
                        keyword={mediaSearchKeyword}
                        sortField={sortField}
                        sortOrder={sortOrder}
                        layoutVersion={layoutVersion}
                        refreshVersion={contentRefreshVersion}
                        filter={{ type: 'watched', value: 'true', label: '已看' }}
                        onSelectMedia={handleSelectMedia}
                        onCountChange={setMediaCount}
                        onQuickPlayStatus={showStatus}
                        onMediaChange={handleListMediaChange}
                        initialScrollTop={currentGridScrollTop}
                        onScrollPositionChange={handleGridScrollPositionChange}
                        mutation={listMutation}
                    />
                );
            case 'favorite':
                return (
                    <MediaGrid
                        libraryId={currentLib.id}
                        keyword={mediaSearchKeyword}
                        sortField={sortField}
                        sortOrder={sortOrder}
                        layoutVersion={layoutVersion}
                        refreshVersion={contentRefreshVersion}
                        filter={{ type: 'favorite', value: 'true', label: '收藏' }}
                        onSelectMedia={handleSelectMedia}
                        onCountChange={setMediaCount}
                        onQuickPlayStatus={showStatus}
                        onMediaChange={handleListMediaChange}
                        initialScrollTop={currentGridScrollTop}
                        onScrollPositionChange={handleGridScrollPositionChange}
                        mutation={listMutation}
                    />
                );
            case 'actor':
                return (
                    <CategoryGrid
                        type="actor"
                        libraryId={currentLib.id}
                        keyword={mediaSearchKeyword}
                        refreshVersion={contentRefreshVersion}
                        sortField={categorySort.field}
                        sortOrder={categorySort.order}
                        viewMode={categoryViewMode}
                        fetchFn={GetActorStats}
                        onSelect={(value, label) => applyFilterFromView('actor', { type: 'actor', value, label })}
                        onStatsChange={handleCategoryStatsChange}
                    />
                );
            case 'genre':
                return (
                    <CategoryGrid
                        type="genre"
                        libraryId={currentLib.id}
                        keyword={searchKeyword}
                        refreshVersion={contentRefreshVersion}
                        sortField={categorySort.field}
                        sortOrder={categorySort.order}
                        fetchFn={GetGenreStats}
                        onSelect={(value, label) => applyFilterFromView('genre', { type: 'genre', value, label })}
                        onStatsChange={handleCategoryStatsChange}
                    />
                );
            case 'settings':
                return <SettingsPage />;
            case 'libs':
            default:
                return (
                    <MediaGrid
                        libraryId={currentLib.id}
                        keyword={searchKeyword}
                        sortField={sortField}
                        sortOrder={sortOrder}
                        layoutVersion={layoutVersion}
                        refreshVersion={contentRefreshVersion}
                        filter={activeFilter}
                        onSelectMedia={handleSelectMedia}
                        onCountChange={setMediaCount}
                        onQuickPlayStatus={showStatus}
                        onMediaChange={handleListMediaChange}
                        initialScrollTop={currentGridScrollTop}
                        onScrollPositionChange={handleGridScrollPositionChange}
                        mutation={listMutation}
                    />
                );
        }
    };

    return (
        <div className="app-container">
            <div className="window-shell">
                <div className="workspace-shell">
                    <Sidebar
                        appName={APP_TITLE}
                        libraries={libraries}
                        currentLib={currentLib}
                        currentView={view}
                        onSelectLib={handleSelectLibrary}
                        onOpenSettings={handleOpenSettings}
                        onSelectView={handleSelectView}
                        onAddLib={() => setShowLibModal(true)}
                        onEditLib={(lib: any) => setEditingLib(lib)}
                    />

                    <div className="main-content">
                        <TopBar
                            hidden={isDetailOpen}
                            title={headerTitle}
                            stats={headerStats}
                            filterKind={activeFilter ? (FILTER_KIND_LABELS[activeFilter.type] || '筛选') : undefined}
                            filterValue={activeFilter?.label}
                            showSearch={view !== 'settings'}
                            searchValue={searchKeyword}
                            onSearch={handleSearchChange}
                            searchPlaceholder={searchPlaceholder}
                            searchDisabled={!searchEnabled || isEmptyListView}
                            compactSearch={view === 'actor'}
                            scanDisabled={Boolean(activeScan) || scanRequestPendingLibraryID === currentLib?.id}
                            onScanWithMode={showLibraryActions ? handleScanWithMode : undefined}
                            onRandomPlay={showListActions ? handleRandomPlay : undefined}
                            onSortSelect={
                                showCategorySort
                                    ? handleCategorySortSelect
                                    : showListActions ? handleSortSelect : undefined
                            }
                            viewMode={categoryViewMode}
                            onToggleViewMode={
                                currentLib && view === 'actor'
                                    ? () => setCategoryViewMode((mode) => (mode === 'grid' ? 'list' : 'grid'))
                                    : undefined
                            }
                            sortField={showCategorySort ? categorySort.field : sortField}
                            sortOrder={showCategorySort ? categorySort.order : sortOrder}
                            sortOptions={
                                showCategorySort
                                    ? CATEGORY_SORT_OPTIONS
                                    : showListActions ? currentSortOptions : undefined
                            }
                            onBackButtonClick={topBarBackAction}
                            backButtonLabel={topBarBackLabel}
                            onClearFilter={activeFilter || searchKeyword.trim() ? clearFilter : undefined}
                            libraryName={currentLibraryName}
                            libraryPath={currentLib?.path || ''}
                            libraryMediaCount={baseCount || 0}
                        />

                        <div className="content-region">
                            {renderWorkspaceContent()}
                        </div>

                        {isDetailOpen && (
                            <div className="detail-overlay-shell">
                                <MediaDetail
                                    media={selectedMedia}
                                    mediaStateUpdate={latestMediaStateUpdate}
                                    libraryName={currentLib?.name || ''}
                                    onClose={() => setSelectedMedia(null)}
                                    onSelectMedia={handleSelectMedia}
                                    onSelectFilter={applyFilterFromDetail}
                                    onMediaChange={handleDetailMediaChange}
                                    onMediaDelete={handleDetailDelete}
                                />
                            </div>
                        )}
                    </div>
                </div>
            </div>

            {showLibModal && (
                <LibraryModal onClose={() => setShowLibModal(false)} onSuccess={handleLibCreated} />
            )}

            {editingLib && (
                <LibraryEditModal
                    library={editingLib}
                    onClose={() => setEditingLib(null)}
                    onSaved={handleLibSaved}
                    onDeleted={handleLibDeleted}
                />
            )}

            {statusToast && (
                <div className={`status-toast ${statusToast.kind}`}>
                    {statusToast.kind === 'error'
                        ? <TriangleAlert size={13} />
                        : statusToast.kind === 'play'
                            ? <Play size={13} />
                            : <Info size={13} />}
                    <span className="status-toast-text" title={statusToast.text}>{statusToast.text}</span>
                    {statusToast.action && (
                        <button type="button" className="status-toast-action" onClick={statusToast.action.onClick}>
                            {statusToast.action.label}
                        </button>
                    )}
                </div>
            )}

            {/* 扫描进行中出现提示条时，扫描卡往上顶，而不是被整个藏掉 */}
            <ScanTaskPanel stacked={Boolean(statusToast)} libraryId={currentLib?.id || ''} />
        </div>
    );
}

export default App;
