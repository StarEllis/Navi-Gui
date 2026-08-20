import React, { useEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react';
import {
    DeleteMedia,
    GetDetailRecommendations,
    GetMediaDetail,
    GetNFOEditorData,
    OpenMediaFolder,
    PlayMedia,
    RestartMedia,
    SaveNFOEditorData,
    SetMyRating,
    ToggleFavorite,
    ToggleWatched,
} from "../../wailsjs/go/main/App";
import { ClipboardSetText, EventsOn, WindowToggleMaximise } from "../../wailsjs/runtime/runtime";
import {
    ArrowLeft,
    Check,
    ChevronDown,
    ChevronLeft,
    ChevronRight,
    Copy,
    Eye,
    EyeOff,
    FilePenLine,
    FileVideo,
    FolderOpen,
    Heart,
    Play,
    Plus,
    RotateCcw,
    Trash2,
    UserRound,
} from 'lucide-react';
import NFOEditModal from './NFOEditModal';
import RecommendationRail from './RecommendationRail';
import StarRating from './StarRating';
import TagPicker from './TagPicker';
import {
    buildCategoryColorMap,
    categoryColor,
    getUserTagsSnapshot,
    invalidateUserTags,
    loadUserTags,
    subscribeUserTags,
} from '../utils/userTags';
import type {
    AppMedia,
    MediaFilter,
    NFOEditorDraft,
    RecommendationGroups,
    RecommendationItem,
} from '../types/wails';
import type { StatusKind } from '../types/status';
import { formatError, toLocalAssetUrl } from '../utils/media';
import { createAssetPrefetcher } from '../utils/assetPrefetch';
import { loadTrailerVolume, persistTrailerVolume } from '../utils/trailerVolume';
import {
    fetchMediaDetailCacheEntry,
    getMediaDetailCacheEntry,
    mergeMediaDetailCacheEntry,
    removeMediaDetailCacheEntry,
    seedMediaDetailCache,
} from '../utils/mediaDetailCache';
import {
    applyMediaStateUpdate,
    formatPlaybackTime,
    getMediaProgressPercent,
    type MediaStateUpdate,
} from '../utils/mediaPlaybackState';

interface MediaDetailProps {
    media: AppMedia;
    mediaStateUpdate?: MediaStateUpdate | null;
    libraryName?: string;
    onClose: () => void;
    onStatus?: (message: string, kind?: StatusKind) => void;
    onSelectLibrary?: () => void;
    onSelectMedia: (media: AppMedia) => void;
    onSelectFilter: (filter: MediaFilter) => void;
    onMediaChange?: (media: AppMedia) => void;
    onMediaDelete?: (mediaID: string) => void;
}

interface DetailActor {
    id?: string;
    name: string;
}

interface CopyFeedback {
    x: number;
    y: number;
}

type DetailImageRole = 'poster' | 'backdrop';

const DETAIL_STICKY_HEIGHT = 60;
const RESUME_PROGRESS_LIMIT = 90;

const formatFileSize = (bytes?: number) => {
    if (typeof bytes !== 'number' || !Number.isFinite(bytes) || bytes <= 0) {
        return '';
    }
    const gb = 1024 ** 3;
    return bytes >= gb
        ? `${(bytes / gb).toFixed(2)} GB`
        : `${(bytes / (1024 ** 2)).toFixed(1)} MB`;
};

const formatDateOnly = (value: unknown) => {
    if (typeof value !== 'string' || !value.trim()) {
        return '';
    }
    const match = value.trim().match(/^\d{4}-\d{2}-\d{2}/);
    return match?.[0] || '';
};

const imageTokenPattern = /[-_.\s]+/;
const coverTokens = ['cover', 'folder', 'thumb', 'movie', 'show'];

const getImageTokens = (path: string) => {
    const filename = path.split(/[\\/]/).pop()?.toLowerCase() || '';
    const stem = filename.replace(/\.[^.]+$/, '');
    return stem.split(imageTokenPattern).filter(Boolean);
};

const hasImageToken = (path: string, token: string) => getImageTokens(path).includes(token);
const isImagePath = (path: unknown): path is string => typeof path === 'string' && path.trim().length > 0;
const isPosterLikeImage = (path: string) => hasImageToken(path, 'poster') || coverTokens.some((token) => hasImageToken(path, token));
const deriveImmediateFanartPath = (filePath: string) => {
    const trimmed = filePath.trim();
    if (!trimmed) {
        return '';
    }

    const ext = trimmed.match(/\.[^.\\/]+$/)?.[0] || '';
    if (!ext) {
        return '';
    }

    return `${trimmed.slice(0, -ext.length)}-fanart.jpg`;
};

const pickDetailImagePath = (detail: AppMedia, previews: string[], role: DetailImageRole) => {
    const backgroundTokens = ['fanart', 'backdrop', 'background', 'banner', 'clearart', 'landscape'];
    const candidates = Array.from(new Set(
        [detail.poster_path, detail.backdrop_path, detail.fanart_path, ...previews]
            .filter((path): path is string => typeof path === 'string' && path.trim().length > 0),
    ));

    const isPosterImage = (path: string) => hasImageToken(path, 'poster');
    const isCoverLikeImage = (path: string) => coverTokens.some((token) => hasImageToken(path, token));
    const isBackgroundImage = (path: string) => backgroundTokens.some((token) => hasImageToken(path, token));

    const ranked = candidates
        .map((path, index) => {
            let priority = Number.POSITIVE_INFINITY;

            if (role === 'poster') {
                if (isPosterImage(path)) priority = 0;
                else if (isCoverLikeImage(path)) priority = 1;
                else if (path === detail.poster_path && !isBackgroundImage(path)) priority = 2;
            } else {
                if (hasImageToken(path, 'fanart')) priority = 0;
                else if (hasImageToken(path, 'backdrop')) priority = 1;
                else if (['background', 'banner', 'clearart', 'landscape'].some((token) => hasImageToken(path, token))) priority = 2;
                else if (path === detail.backdrop_path && !isPosterImage(path) && !isCoverLikeImage(path)) priority = 3;
            }

            return { path, index, priority };
        })
        .filter((candidate) => Number.isFinite(candidate.priority))
        .sort((left, right) => left.priority - right.priority || left.index - right.index);

    return ranked[0]?.path || '';
};

const pickPosterImagePath = (detail: AppMedia, previews: string[]) => {
    if (isImagePath(detail.poster_path)) {
        return detail.poster_path.trim();
    }
    return pickDetailImagePath(detail, previews, 'poster');
};

const pickBackdropImagePath = (detail: AppMedia) => {
    const posterPath = isImagePath(detail.poster_path) ? detail.poster_path.trim() : '';

    return [detail.fanart_path, detail.backdrop_path]
        .filter(isImagePath)
        .map((path) => path.trim())
        .find((path) => path !== posterPath && !isPosterLikeImage(path)) || '';
};

const formatActorName = (name: string) => {
    if (!name) {
        return '';
    }
    return name.replace(/[?？\s]+$/, '').replace(/\(\d+\)$/, '').trim();
};

const cleanOverview = (text: string) => {
    if (!text) {
        return '暂无简介';
    }
    return text
        .replace(/<!\[CDATA\[/g, '')
        .replace(/\]\]>/g, '')
        .replace(/<br\s*\/?>/gi, '\n')
        .replace(/<[^>]+>/g, '')
        .trim();
};

const normalizeMetadataPhase = (phase?: string) => {
    switch ((phase || '').trim().toLowerCase()) {
        case 'quick':
            return 'quick';
        case 'failed':
            return 'failed';
        default:
            return 'full';
    }
};

const getMetadataFallback = (detail: AppMedia, fallback: string) => {
    const phase = normalizeMetadataPhase(detail.metadata_phase);
    if (phase === 'quick') {
        return '补全中';
    }
    if (phase === 'failed') {
        return '补全失败';
    }
    return fallback;
};

const getMediaCode = (detail: AppMedia, currFilePath: string) => {
    if (detail.nfo_extra_fields) {
        try {
            const extra = JSON.parse(detail.nfo_extra_fields);
            if (extra.num) {
                return String(extra.num).toUpperCase();
            }
        } catch (_error) {
            // ignore malformed nfo_extra_fields and keep fallback chain below
        }
    }

    const filename = currFilePath?.split(/[\\/]/).pop() || '';
    const codeMatch = filename.match(/([A-Z0-9]{2,10}-\d{2,6})/i);
    if (codeMatch) {
        return codeMatch[1].toUpperCase();
    }

    return detail.code || detail.id?.slice(0, 8) || '未知';
};

const normalizeActors = (detail: AppMedia): DetailActor[] => {
    if (Array.isArray(detail.actors) && detail.actors.length > 0) {
        return detail.actors
            .map((actor) => ({
                id: actor?.id,
                name: formatActorName(actor?.name || ''),
            }))
            .filter((actor: DetailActor) => actor.name);
    }

    if (detail.actor) {
        return String(detail.actor)
            .split(/[,，/]/)
            .map((name: string) => ({ name: formatActorName(name) }))
            .filter((actor: DetailActor) => actor.name);
    }

    return [];
};

const TECHNICAL_KEYWORDS = [
    '4K', '1080P', '720P', 'UHD', 'HD', 'FHD', 'SD',
    'H265', 'HEVC', 'H264', 'X264', 'X265', 'AV1', 'HDR',
    '中文字幕', '字幕', '60FPS', 'FPS', '无码', '破解', '流出', 'REMUX', 'WEB-DL',
];

const CORE_KEYWORDS = [
    '剧情', '恋爱', '人妻', '素人', '学生', '老师', '护士', '秘书', 'OL',
    '校园', '职场', '旅行', '温泉', '家庭', '情侣', '制服', '巨乳',
    '熟女', '姐姐', '妹妹', '偶像', '角色', '人物',
];

const isTechnicalTag = (tag: string) => {
    const upper = tag.toUpperCase();
    return TECHNICAL_KEYWORDS.some((keyword) => upper.includes(keyword) || tag.includes(keyword));
};

/** chip 文案比对用：忽略大小写、空白与全半角冒号，用来判断固定 chip 是否已在 NFO 标签里。 */
const normalizeTagKey = (tag: string) => tag.replace(/[：:]/g, ':').replace(/\s+/g, '').toLowerCase();

/** 技术规格（1080P / HEVC / 中文字幕…）与内容标签分成两档：前者进番号行，后者才做 chip。 */
const splitDetailTags = (detail: AppMedia) => {
    const rawTags = detail.genres
        ? String(detail.genres).split(/[,，/]/).map((tag: string) => tag.trim()).filter(Boolean)
        : [];

    const seen = new Set<string>();
    const uniqueTags = rawTags.filter((tag: string) => {
        const key = tag.toLowerCase();
        if (seen.has(key)) {
            return false;
        }
        seen.add(key);
        return true;
    });

    const scoreTag = (tag: string) => {
        const isCore = CORE_KEYWORDS.some((keyword) => tag.includes(keyword));
        return (isCore ? 0 : 80) + tag.length;
    };

    return {
        technical: uniqueTags.filter(isTechnicalTag),
        content: uniqueTags
            .filter((tag: string) => !isTechnicalTag(tag))
            .sort((left: string, right: string) => scoreTag(left) - scoreTag(right)),
    };
};

/** 番号行右侧那串规格：分辨率、编码在前，NFO 里的技术标签在后，忽略大小写去重。 */
const buildTechnicalSpecs = (detail: AppMedia, technicalTags: string[]) => {
    const specs: string[] = [];
    const seen = new Set<string>();

    [detail.resolution, detail.video_codec, ...technicalTags].forEach((value) => {
        const text = (typeof value === 'string' ? value : '').trim();
        if (!text) {
            return;
        }
        const key = text.toUpperCase();
        if (seen.has(key)) {
            return;
        }
        seen.add(key);
        specs.push(/^[a-z0-9.\-]+$/i.test(text) ? key : text);
    });

    return specs;
};

const getRecommendationMediaKey = (item: RecommendationItem) => {
    const media = item.media;

    if (typeof media?.id === 'string' && media.id.trim()) {
        return `id:${media.id.trim()}`;
    }

    if (typeof media?.file_path === 'string' && media.file_path.trim()) {
        return `file:${media.file_path.trim().toLowerCase()}`;
    }

    if (typeof media?.code === 'string' && media.code.trim()) {
        return `code:${media.code.trim().toLowerCase()}`;
    }

    if (typeof media?.title === 'string' && media.title.trim()) {
        return `title:${media.title.trim().toLowerCase()}`;
    }

    return '';
};

const mergeRecommendationItems = (recommendationGroups?: RecommendationGroups | null) => {
    const merged: RecommendationItem[] = [];
    const seen = new Set<string>();

    [recommendationGroups?.continue_watching, recommendationGroups?.more_like_this].forEach((group, groupIndex) => {
        if (!Array.isArray(group)) {
            return;
        }

        group.forEach((item, itemIndex) => {
            const key = getRecommendationMediaKey(item) || `fallback:${groupIndex}:${itemIndex}`;
            if (seen.has(key)) {
                return;
            }

            seen.add(key);
            merged.push(item);
        });
    });

    return merged;
};

const emptyRecommendations: RecommendationGroups = { continue_watching: [], more_like_this: [] };

const applyMediaStateToRecommendations = (
    groups: RecommendationGroups,
    update: MediaStateUpdate,
) => {
    let changed = false;
    const applyToGroup = (items: RecommendationItem[]) => items.map((item) => {
        const nextMedia = applyMediaStateUpdate(item.media as AppMedia & Record<string, any>, update) as AppMedia;
        if (nextMedia === item.media) {
            return item;
        }
        changed = true;
        return { ...item, media: nextMedia };
    });
    const next = {
        ...groups,
        continue_watching: applyToGroup(groups.continue_watching || []),
        more_like_this: applyToGroup(groups.more_like_this || []),
    };
    return changed ? next : groups;
};

const applyKnownMediaStatesToRecommendations = (
    groups: RecommendationGroups,
    updates: Map<string, MediaStateUpdate>,
) => {
    let next = groups;
    updates.forEach((update) => {
        next = applyMediaStateToRecommendations(next, update);
    });
    return next;
};

const MediaDetail: React.FC<MediaDetailProps> = ({
    media,
    mediaStateUpdate,
    libraryName,
    onClose,
    onStatus,
    onSelectLibrary,
    onSelectMedia,
    onSelectFilter,
    onMediaChange,
    onMediaDelete,
}) => {
    const [detail, setDetail] = useState(media);
    const [isOverviewExpanded, setIsOverviewExpanded] = useState(false);
    const [files, setFiles] = useState<string[]>([]);
    const [previews, setPreviews] = useState<string[]>([]);
    const [trailer, setTrailer] = useState('');
    const prefetchTrailerRef = useRef(createAssetPrefetcher());
    const [recommendations, setRecommendations] = useState<RecommendationGroups>(emptyRecommendations);
    const [recommendationLoading, setRecommendationLoading] = useState(false);
    const [currFilePath, setCurrFilePath] = useState(media.file_path || '');
    const [showFileMenu, setShowFileMenu] = useState(false);
    const [showNFOEditor, setShowNFOEditor] = useState(false);
    const [nfoEditorData, setNfoEditorData] = useState<NFOEditorDraft | null>(null);
    const [nfoLoading, setNfoLoading] = useState(false);
    const [nfoSaving, setNfoSaving] = useState(false);
    const [previewViewerIndex, setPreviewViewerIndex] = useState<number | null>(null);
    const [ratingFlash, setRatingFlash] = useState(false);
    const [tagPickerRect, setTagPickerRect] = useState<DOMRect | null>(null);
    const myTagsRowRef = useRef<HTMLDivElement | null>(null);
    const userTags = useSyncExternalStore(subscribeUserTags, getUserTagsSnapshot);
    const categoryColors = useMemo(() => buildCategoryColorMap(userTags), [userTags]);
    const [codeCopyFeedback, setCodeCopyFeedback] = useState<CopyFeedback | null>(null);
    const [isStickyBarVisible, setIsStickyBarVisible] = useState(false);
    const fileDropdownRef = useRef<HTMLDivElement | null>(null);
    const codeCopyTimerRef = useRef<number | null>(null);
    // 详情页整页滚动容器，同时是吸顶栏 IntersectionObserver 的 root
    const scrollRef = useRef<HTMLDivElement | null>(null);
    const titleRef = useRef<HTMLHeadingElement | null>(null);
    const mediaStateByIDRef = useRef(new Map<string, MediaStateUpdate>());

    // 提示条统一交给 App 渲染。详情页自己渲染的话会被关进 .detail-overlay-shell
    // 的层叠上下文里，扫描卡和 NFO 弹窗都压在它上面，保存失败根本看不见。
    const showMsg = (message: string, kind: StatusKind = 'info') => {
        onStatus?.(message, kind);
    };

    const clearCodeCopyFeedbackTimer = () => {
        if (codeCopyTimerRef.current !== null) {
            window.clearTimeout(codeCopyTimerRef.current);
            codeCopyTimerRef.current = null;
        }
    };

    const applyResolvedDetail = (nextDetail: AppMedia) => {
        const knownState = mediaStateByIDRef.current.get(nextDetail.id);
        const resolvedDetail = knownState
            ? applyMediaStateUpdate(nextDetail as AppMedia & Record<string, any>, knownState) as AppMedia
            : nextDetail;
        setDetail(resolvedDetail);
        setCurrFilePath((prev) => (prev || resolvedDetail.file_path || media.file_path || '').trim());
        mergeMediaDetailCacheEntry(resolvedDetail);
        onMediaChange?.(resolvedDetail);
    };

    useEffect(() => {
        if (!mediaStateUpdate) {
            return;
        }
        const current = mediaStateByIDRef.current.get(mediaStateUpdate.id) || { id: mediaStateUpdate.id };
        const next = applyMediaStateUpdate(current, mediaStateUpdate);
        if (next === current) {
            return;
        }
        mediaStateByIDRef.current.delete(mediaStateUpdate.id);
        mediaStateByIDRef.current.set(mediaStateUpdate.id, next);
        while (mediaStateByIDRef.current.size > 64) {
            const oldestMediaID = mediaStateByIDRef.current.keys().next().value;
            if (!oldestMediaID) {
                break;
            }
            mediaStateByIDRef.current.delete(oldestMediaID);
        }
        setDetail((currentDetail) => (
            applyMediaStateUpdate(currentDetail as AppMedia & Record<string, any>, mediaStateUpdate) as AppMedia
        ));
        setRecommendations((currentGroups) => applyMediaStateToRecommendations(currentGroups, mediaStateUpdate));
    }, [mediaStateUpdate]);

    useEffect(() => {
        let active = true;
        const cachedEntry = getMediaDetailCacheEntry(media.id);
        const fallbackFiles = cachedEntry?.files?.length
            ? cachedEntry.files
            : (typeof media.file_path === 'string' && media.file_path.trim() ? [media.file_path.trim()] : []);
        const fallbackPreviews = cachedEntry?.previews || [];
        const fallbackTrailer = cachedEntry?.trailer || '';
        const initialDetail = cachedEntry?.detail || media;
        const knownInitialState = mediaStateByIDRef.current.get(media.id);
        const resolvedInitialDetail = knownInitialState
            ? applyMediaStateUpdate(initialDetail as AppMedia & Record<string, any>, knownInitialState) as AppMedia
            : initialDetail;

        seedMediaDetailCache(media);
        setDetail(resolvedInitialDetail);
        setFiles(fallbackFiles);
        setPreviews(fallbackPreviews);
        setTrailer(fallbackTrailer);
        setCurrFilePath((cachedEntry?.detail?.file_path || fallbackFiles[0] || media.file_path || '').trim());
        setRecommendations(emptyRecommendations);
        setRecommendationLoading(true);
        setCodeCopyFeedback(null);
        setIsOverviewExpanded(false);
        setIsStickyBarVisible(false);
        clearCodeCopyFeedbackTimer();
        // 从推荐位跳到下一部时，详情页得回到首屏，否则吸顶栏一进来就是显示态。
        if (scrollRef.current) {
            scrollRef.current.scrollTop = 0;
        }

        GetDetailRecommendations(media.id, 12)
            .then((nextRecommendations) => {
                if (!active) {
                    return;
                }
                setRecommendations(applyKnownMediaStatesToRecommendations(
                    nextRecommendations || emptyRecommendations,
                    mediaStateByIDRef.current,
                ));
            })
            .catch((error) => {
                console.error(error);
                if (!active) {
                    return;
                }
                setRecommendations(emptyRecommendations);
            })
            .finally(() => {
                if (active) {
                    setRecommendationLoading(false);
                }
            });

        const load = async () => {
            try {
                const nextEntry = await fetchMediaDetailCacheEntry(media.id);

                if (!active) {
                    return;
                }

                const knownState = mediaStateByIDRef.current.get(media.id);
                const resolvedDetail = knownState
                    ? applyMediaStateUpdate(nextEntry.detail as AppMedia & Record<string, any>, knownState) as AppMedia
                    : nextEntry.detail;
                setDetail(resolvedDetail);
                setFiles(nextEntry.files);
                setPreviews(nextEntry.previews);
                setTrailer(nextEntry.trailer);
                setCurrFilePath((resolvedDetail?.file_path || nextEntry.files[0] || media.file_path || '').trim());
                onMediaChange?.(resolvedDetail);
            } catch (error) {
                console.error(error);
                if (!active) {
                    return;
                }
                showMsg(`加载详情失败：${formatError(error)}`, 'error');
            }
        };

        load();
        return () => {
            active = false;
        };
    }, [media.id, media.file_path]);

    useEffect(() => {
        const onPointerDown = (event: MouseEvent) => {
            if (!fileDropdownRef.current?.contains(event.target as Node)) {
                setShowFileMenu(false);
            }
        };

        const onEscape = (event: KeyboardEvent) => {
            if (event.key === 'Escape') {
                setShowFileMenu(false);
            }
        };

        document.addEventListener('mousedown', onPointerDown);
        document.addEventListener('keydown', onEscape);
        return () => {
            document.removeEventListener('mousedown', onPointerDown);
            document.removeEventListener('keydown', onEscape);
        };
    }, []);

    // 首屏标题滚出可视区后，吸顶栏才淡入。
    useEffect(() => {
        const scrollNode = scrollRef.current;
        const titleNode = titleRef.current;
        if (!scrollNode || !titleNode) {
            return;
        }

        const observer = new IntersectionObserver(
            ([entry]) => setIsStickyBarVisible(!entry.isIntersecting),
            { root: scrollNode, rootMargin: `-${DETAIL_STICKY_HEIGHT}px 0px 0px 0px`, threshold: 0 },
        );
        observer.observe(titleNode);

        return () => {
            observer.disconnect();
        };
    }, []);

    // 预告片排在剧照前面：它跟剧照同属一组「预览」，但要用 video 播放。
    const previewItems = useMemo(() => {
        const stills = previews.map((path) => ({ path, isVideo: false }));
        return trailer ? [{ path: trailer, isVideo: true }, ...stills] : stills;
    }, [previews, trailer]);

    useEffect(() => {
        if (previewViewerIndex === null) {
            return;
        }

        const onKeyDown = (event: KeyboardEvent) => {
            if (event.key === 'Escape') {
                setPreviewViewerIndex(null);
                return;
            }

            if (event.key === 'ArrowLeft') {
                setPreviewViewerIndex((currentIndex) => {
                    if (currentIndex === null || currentIndex <= 0) {
                        return currentIndex;
                    }
                    return currentIndex - 1;
                });
                return;
            }

            if (event.key === 'ArrowRight') {
                setPreviewViewerIndex((currentIndex) => {
                    if (currentIndex === null || currentIndex >= previewItems.length - 1) {
                        return currentIndex;
                    }
                    return currentIndex + 1;
                });
            }
        };

        document.addEventListener('keydown', onKeyDown);
        return () => {
            document.removeEventListener('keydown', onKeyDown);
        };
    }, [previewViewerIndex, previewItems.length]);

    useEffect(() => {
        if (previewViewerIndex !== null && previewViewerIndex >= previewItems.length) {
            setPreviewViewerIndex(previewItems.length > 0 ? previewItems.length - 1 : null);
        }
    }, [previewViewerIndex, previewItems.length]);

    useEffect(() => {
        return () => {
            clearCodeCopyFeedbackTimer();
        };
    }, []);

    useEffect(() => {
        const unsubscribe = EventsOn("media:metadata-updated", (data: any) => {
            if (data?.media_id !== media.id) {
                return;
            }

            void refreshDetailAndPreviews().catch((error) => {
                console.error(error);
            });
        });

        return () => {
            unsubscribe();
        };
    }, [media.id]);

    const refreshDetail = async () => {
        const nextDetail = await GetMediaDetail(media.id);
        applyResolvedDetail(nextDetail);
    };

    const refreshDetailAndPreviews = async () => {
        const nextEntry = await fetchMediaDetailCacheEntry(media.id);
        setDetail(nextEntry.detail);
        setFiles(nextEntry.files);
        setPreviews(nextEntry.previews);
        setTrailer(nextEntry.trailer);
        setCurrFilePath((prev) => (prev || nextEntry.detail.file_path || media.file_path || '').trim());
        onMediaChange?.(nextEntry.detail);
    };

    const refreshRecommendations = async () => {
        setRecommendationLoading(true);
        try {
            const nextRecommendations = await GetDetailRecommendations(media.id, 12);
            setRecommendations(nextRecommendations || emptyRecommendations);
        } catch (error) {
            console.error(error);
            setRecommendations(emptyRecommendations);
            showMsg(`加载推荐失败：${formatError(error)}`, 'error');
        } finally {
            setRecommendationLoading(false);
        }
    };

    const handlePlay = async () => {
        const targetPath = (currFilePath || detail.file_path || media.file_path || '').trim();
        if (!targetPath) {
            showMsg('播放失败：当前没有可播放文件', 'error');
            return;
        }

        try {
            showMsg(`正在启动播放器：${targetPath.split(/[\\/]/).pop()}`, 'play');
            await PlayMedia(detail.id, targetPath);
            await refreshDetail();
        } catch (error) {
            console.error(error);
            showMsg(`播放失败：${formatError(error)}`, 'error');
        }
    };

    const handleRestart = async () => {
        const targetPath = (currFilePath || detail.file_path || media.file_path || '').trim();
        if (!targetPath) {
            showMsg('播放失败：当前没有可播放文件', 'error');
            return;
        }

        try {
            showMsg(`正在从头播放：${targetPath.split(/[\\/]/).pop()}`, 'play');
            await RestartMedia(detail.id, targetPath);
            setDetail((currentDetail) => {
                const nextDetail = {
                    ...currentDetail,
                    position: 0,
                    progress_percent: 0,
                    playback_state: 'starting',
                };
                mergeMediaDetailCacheEntry(nextDetail);
                onMediaChange?.(nextDetail);
                return nextDetail;
            });
        } catch (error) {
            console.error(error);
            showMsg(`从头播放失败：${formatError(error)}`, 'error');
        }
    };

    const handleOpenDir = async () => {
        try {
            await OpenMediaFolder(detail.id);
        } catch (error) {
            console.error(error);
            showMsg(`打开目录失败：${formatError(error)}`, 'error');
        }
    };

    const handleOpenNFO = async () => {
        try {
            setShowNFOEditor(true);
            setNfoLoading(true);
            const nextData = await GetNFOEditorData(detail.id);
            setNfoEditorData(nextData);
        } catch (error) {
            console.error(error);
            setShowNFOEditor(false);
            showMsg(`打开 NFO 失败：${formatError(error)}`, 'error');
        } finally {
            setNfoLoading(false);
        }
    };

    const handleSaveNFO = async (draft: NFOEditorDraft) => {
        try {
            setNfoSaving(true);
            await SaveNFOEditorData(detail.id, draft);
            await refreshDetail();
            await refreshRecommendations();
            setNfoEditorData(draft);
            setShowNFOEditor(false);
            showMsg('NFO 已保存');
        } catch (error) {
            console.error(error);
            const message = formatError(error);
            if (message.includes('UnsupportedNFOLayout')) {
                showMsg('为避免数据丢失，此 NFO 结构暂不支持编辑', 'error');
            } else {
                showMsg(`保存 NFO 失败：${message}`, 'error');
            }
        } finally {
            setNfoSaving(false);
        }
    };

    const handleDelete = async () => {
		if (!window.confirm('确定要从数据库中移除此条目吗？不会删除影片文件；会清理 Navi 生成的缩略图。')) {
            return;
        }

        try {
            await DeleteMedia(detail.id);
            removeMediaDetailCacheEntry(detail.id);
            onMediaDelete?.(detail.id);
            onClose();
        } catch (error) {
            console.error(error);
            showMsg(`删除失败：${formatError(error)}`, 'error');
        }
    };

    const myRating = Number.isFinite(Number(detail?.my_rating)) ? Math.max(0, Number(detail.my_rating)) : 0;
    const myTags: any[] = Array.isArray((detail as any)?.my_tags) ? (detail as any).my_tags : [];
    const myTagAssignment = useMemo(() => {
        const counts: Record<string, number> = {};
        myTags.forEach((tag: any) => {
            if (tag?.id) {
                counts[tag.id] = 1;
            }
        });
        return counts;
    }, [myTags]);

    const handleRate = async (score: number) => {
        try {
            await SetMyRating(detail.id, score);
            const nextScore = myRating === score ? 0 : score;
            setDetail((prev) => {
                const nextDetail = { ...prev, my_rating: nextScore };
                mergeMediaDetailCacheEntry(nextDetail);
                onMediaChange?.(nextDetail);
                return nextDetail;
            });
            // 数字从灰闪一下到亮再回落，代替确认弹窗
            setRatingFlash(true);
            window.setTimeout(() => setRatingFlash(false), 260);
        } catch (error) {
            console.error(error);
            showMsg(`打分失败：${formatError(error)}`, 'error');
        }
    };

    useEffect(() => {
        void loadUserTags();
    }, []);

    // 1–5 打分、0 清空、T 开标签 popover；输入框聚焦或有弹层时不响应
    useEffect(() => {
        const onKeyDown = (event: KeyboardEvent) => {
            if (event.ctrlKey || event.metaKey || event.altKey) {
                return;
            }
            const target = event.target as HTMLElement | null;
            if (target && (
                target.isContentEditable
                || ['INPUT', 'TEXTAREA', 'SELECT'].includes(target.tagName)
            )) {
                return;
            }
            if (previewViewerIndex !== null || showNFOEditor || tagPickerRect) {
                return;
            }
            if (event.key >= '0' && event.key <= '5') {
                event.preventDefault();
                void handleRate(Number(event.key));
                return;
            }
            if (event.key === 't' || event.key === 'T') {
                event.preventDefault();
                const anchor = myTagsRowRef.current?.querySelector('.navi-my-tag-add');
                if (anchor) {
                    setTagPickerRect(anchor.getBoundingClientRect());
                }
            }
        };

        document.addEventListener('keydown', onKeyDown);
        return () => document.removeEventListener('keydown', onKeyDown);
    });

    const handleFav = async () => {
        try {
            await ToggleFavorite(detail.id);
            setDetail((prev) => {
                const nextDetail = { ...prev, is_favorite: !prev.is_favorite };
                mergeMediaDetailCacheEntry(nextDetail);
                onMediaChange?.(nextDetail);
                return nextDetail;
            });
        } catch (error) {
            console.error(error);
            showMsg(`收藏失败：${formatError(error)}`, 'error');
        }
    };

    const handleWatched = async () => {
        try {
            await ToggleWatched(detail.id);
            setDetail((prev) => {
                const nextDetail = { ...prev, is_watched: !prev.is_watched };
                mergeMediaDetailCacheEntry(nextDetail);
                onMediaChange?.(nextDetail);
                return nextDetail;
            });
        } catch (error) {
            console.error(error);
            showMsg(`更新观看状态失败：${formatError(error)}`, 'error');
        }
    };

    // 预告片音量记在 localStorage 里：每个 <video> 都是新元素，不接管的话
    // 每次点开都回到满音量，换一部片子也一样。
    const applyTrailerVolume = (video: HTMLVideoElement | null) => {
        if (!video) {
            return;
        }
        const preference = loadTrailerVolume();
        video.volume = preference.volume;
        video.muted = preference.muted;
    };

    const handleTrailerVolumeChange = (event: React.SyntheticEvent<HTMLVideoElement>) => {
        persistTrailerVolume({
            volume: event.currentTarget.volume,
            muted: event.currentTarget.muted,
        });
    };

    const handleOpenPreviewViewer = (index: number) => {
        setPreviewViewerIndex(index);
    };

    const handleClosePreviewViewer = () => {
        setPreviewViewerIndex(null);
    };

    const handlePreviewViewerPrev = () => {
        setPreviewViewerIndex((currentIndex) => {
            if (currentIndex === null || currentIndex <= 0) {
                return currentIndex;
            }
            return currentIndex - 1;
        });
    };

    const handlePreviewViewerNext = () => {
        setPreviewViewerIndex((currentIndex) => {
            if (currentIndex === null || currentIndex >= previewItems.length - 1) {
                return currentIndex;
            }
            return currentIndex + 1;
        });
    };

    const copyTextWithFeedback = async (text: string, event: React.MouseEvent<HTMLButtonElement>) => {
        const value = text.trim();
        if (!value) {
            return;
        }

        try {
            const copied = await ClipboardSetText(value);
            if (!copied) {
                clearCodeCopyFeedbackTimer();
                setCodeCopyFeedback(null);
                return;
            }

            setCodeCopyFeedback({
                x: Math.round(Math.min(window.innerWidth - 96, Math.max(12, event.clientX + 10))),
                y: Math.round(Math.min(window.innerHeight - 24, Math.max(12, event.clientY))),
            });
            clearCodeCopyFeedbackTimer();
            codeCopyTimerRef.current = window.setTimeout(() => {
                setCodeCopyFeedback(null);
                codeCopyTimerRef.current = null;
            }, 1200);
        } catch (error) {
            console.error(error);
            clearCodeCopyFeedbackTimer();
            setCodeCopyFeedback(null);
            return;
        }
    };

    const posterPath = pickPosterImagePath(detail, previews);
    const immediateBackdropPath = deriveImmediateFanartPath(currFilePath || media.file_path || '');
    const backdropPath = pickBackdropImagePath(detail) || immediateBackdropPath || posterPath;
    const posterUrl = posterPath ? toLocalAssetUrl(posterPath) : '';
    const trailerThumbPath = backdropPath || previews[0] || '';
    const trailerThumbUrl = trailerThumbPath ? toLocalAssetUrl(trailerThumbPath) : '';
    const backdropUrl = backdropPath ? toLocalAssetUrl(backdropPath) : '';
    const actors = normalizeActors(detail);
    const { technical: technicalTags, content: contentTags } = splitDetailTags(detail);
    const technicalSpecs = buildTechnicalSpecs(detail, technicalTags);
    const filename = currFilePath?.split(/[\\/]/).pop() || '未知文件';
    const mediaCode = getMediaCode(detail, currFilePath);
    const detailSeries = detail.series;
    const studioLabel = (detail.studio || detail.publisher || '').trim();
    const makerLabel = (detail.maker || detail.label || '').trim();
    const contentTagKeys = new Set(contentTags.map(normalizeTagKey));
    const hasContentTag = (label: string) => contentTagKeys.has(normalizeTagKey(label));
    const metadataPhase = normalizeMetadataPhase(detail.metadata_phase);
    const metadataHint = metadataPhase === 'quick'
        ? '正在后台补全时长、演员和技术信息…'
        : metadataPhase === 'failed'
            ? '元数据补全失败，可重新扫描后再试。'
            : '';
    const overviewText = cleanOverview(detail.overview);
    const isPreviewViewerOpen = previewViewerIndex !== null;
    const currentPreviewItem = previewViewerIndex !== null ? previewItems[previewViewerIndex] : null;
    const currentPreviewPath = currentPreviewItem?.path || '';
    const hasPreviewNavigation = previewItems.length > 1;
    const canViewPrevPreview = previewViewerIndex !== null && previewViewerIndex > 0;
    const canViewNextPreview = previewViewerIndex !== null && previewViewerIndex < previewItems.length - 1;
    const mergedRecommendations = mergeRecommendationItems(recommendations);
    const playbackProgress = getMediaProgressPercent(detail);
    const playbackDuration = detail.watch_duration || detail.duration;
    const showPlaybackProgress = playbackProgress !== null && playbackProgress > 0;
    const resumePosition = typeof detail.position === 'number' && detail.position > 0 ? detail.position : 0;
    const isPlaybackComplete = resumePosition > 0
        && playbackProgress !== null
        && playbackProgress >= RESUME_PROGRESS_LIMIT;
    const hasResumePoint = resumePosition > 0 && !isPlaybackComplete;
    const playLabel = isPlaybackComplete
        ? '重新观看'
        : hasResumePoint
            ? `继续播放 ${formatPlaybackTime(resumePosition)}`
            : '播放';
    const primaryActor = actors[0];
    const runtimeLabel = detail.duration
        ? `${Math.floor(detail.duration / 60)} min`
        : (detail.runtime ? `${detail.runtime} min` : getMetadataFallback(detail, '未知'));
    const releaseLabel = studioLabel || makerLabel || getMetadataFallback(detail, '未知');
    const releaseDateLabel = detail.release_date_normalized || detail.year || getMetadataFallback(detail, '未知');
    const fileSizeLabel = formatFileSize(detail.file_size);
    const addedDateLabel = formatDateOnly(detail.created_at);
    const hasFileFacts = Boolean(fileSizeLabel || addedDateLabel);
    const isOverviewCollapsible = overviewText.length > 90;

    return (
        <>
            <div className="navi-detail">
                <div
                    className="navi-detail-backdrop"
                    aria-hidden="true"
                    style={backdropUrl ? { backgroundImage: `url("${backdropUrl}")` } : undefined}
                />
                <div className="navi-detail-scrim" aria-hidden="true" />

                <div className="navi-detail-body" ref={scrollRef}>
                    <div className="navi-detail-topbar" onDoubleClick={WindowToggleMaximise}>
                        <button type="button" className="navi-back-btn" onClick={onClose} title="返回列表" aria-label="返回列表">
                            <ArrowLeft size={15} />
                        </button>

                        <div className="navi-breadcrumb">
                            {libraryName && (
                                <>
                                    <button
                                        type="button"
                                        className="navi-breadcrumb-link"
                                        title={libraryName}
                                        onClick={onSelectLibrary || onClose}
                                    >
                                        {libraryName}
                                    </button>
                                    <span>/</span>
                                </>
                            )}
                            {primaryActor && (
                                <>
                                    <button
                                        type="button"
                                        className="navi-breadcrumb-link"
                                        onClick={() => onSelectFilter({
                                            type: 'actor',
                                            value: primaryActor.id || primaryActor.name,
                                            label: primaryActor.name,
                                        })}
                                    >
                                        {primaryActor.name}
                                    </button>
                                    <span>/</span>
                                </>
                            )}
                            <span className="navi-breadcrumb-current">{mediaCode}</span>
                        </div>
                    </div>

                    <div className="navi-detail-hero">
                        <div className="navi-detail-aside">
                            <div className="navi-detail-poster">
                                {posterUrl && <img src={posterUrl} alt="poster" />}
                            </div>
                        </div>

                        <div className="navi-detail-main">
                            <div className="navi-detail-eyebrow">
                                {showPlaybackProgress && playbackProgress !== null && (
                                    <span className="navi-detail-progress-chip">看到 {Math.round(playbackProgress)}%</span>
                                )}
                                <button
                                    type="button"
                                    className="navi-detail-code"
                                    onClick={(event) => void copyTextWithFeedback(mediaCode, event)}
                                    aria-label={`Copy code ${mediaCode}`}
                                    title="点击复制"
                                >
                                    {mediaCode}
                                </button>
                                {technicalSpecs.length > 0 && (
                                    <span className="navi-detail-specs">{technicalSpecs.join(' · ')}</span>
                                )}
                            </div>

                            <h1 className="navi-detail-title" ref={titleRef}>{detail.title}</h1>

                            <div className="navi-detail-actions">
                                <div className="navi-detail-play-group">
                                    <button type="button" className="main" onClick={handlePlay}>
                                        <Play size={17} fill="currentColor" />
                                        <span>{playLabel}</span>
                                    </button>
                                    {hasResumePoint && (
                                        <button
                                            type="button"
                                            className="restart"
                                            title="从头重看"
                                            aria-label="从头重看"
                                            onClick={handleRestart}
                                        >
                                            <RotateCcw size={16} />
                                        </button>
                                    )}
                                </div>
                                <button
                                    type="button"
                                    className={`navi-outline-btn ${detail.is_favorite ? 'on' : ''}`.trim()}
                                    onClick={handleFav}
                                >
                                    <Heart size={16} fill={detail.is_favorite ? 'currentColor' : 'none'} />
                                    <span>{detail.is_favorite ? '已收藏' : '收藏'}</span>
                                </button>
                                <button
                                    type="button"
                                    className={`navi-outline-btn ${detail.is_watched ? 'on' : ''}`.trim()}
                                    onClick={handleWatched}
                                >
                                    {detail.is_watched ? <EyeOff size={16} /> : <Eye size={16} />}
                                    <span>{detail.is_watched ? '标记未看' : '标记已看'}</span>
                                </button>

                                <span className="navi-detail-rating-divider" aria-hidden="true" />

                                <StarRating
                                    value={myRating}
                                    size={19}
                                    onChange={(score) => void handleRate(score)}
                                />
                                {myRating > 0 && (
                                    <span className={`navi-detail-rating-score ${ratingFlash ? 'flash' : ''}`.trim()}>
                                        {myRating}
                                    </span>
                                )}

                                <span className="navi-detail-actions-divider" aria-hidden="true" />

                                <button
                                    type="button"
                                    className="navi-file-btn"
                                    title="打开文件所在目录"
                                    aria-label="打开文件所在目录"
                                    onClick={handleOpenDir}
                                >
                                    <FolderOpen size={16} />
                                </button>
                                <button
                                    type="button"
                                    className="navi-file-btn"
                                    title="编辑 NFO"
                                    aria-label="编辑 NFO"
                                    onClick={handleOpenNFO}
                                >
                                    <FilePenLine size={16} />
                                </button>
                                <button
                                    type="button"
                                    className="navi-file-btn danger"
                                    title="仅从数据库移除这条记录，不删除本地文件"
                                    aria-label="从数据库移除"
                                    onClick={handleDelete}
                                >
                                    <Trash2 size={16} />
                                </button>
                            </div>

                            <div className="navi-detail-file-block">
                                <div className="navi-detail-file" ref={fileDropdownRef}>
                                    <FileVideo size={15} />
                                    <span className="navi-detail-file-name" title={currFilePath}>{filename}</span>

                                    {files.length > 1 && (
                                        <>
                                            <span className="navi-detail-file-count">{files.length} 个文件</span>
                                            <button
                                                type="button"
                                                className="navi-detail-file-toggle"
                                                title="切换文件"
                                                aria-label="切换文件"
                                                onClick={() => setShowFileMenu((open) => !open)}
                                            >
                                                <ChevronDown size={14} />
                                            </button>
                                        </>
                                    )}

                                    <button
                                        type="button"
                                        className="navi-detail-file-toggle"
                                        title="复制完整路径"
                                        aria-label="复制完整路径"
                                        onClick={(event) => void copyTextWithFeedback(currFilePath, event)}
                                    >
                                        <Copy size={13} />
                                    </button>

                                    {showFileMenu && (
                                        <div className="navi-detail-file-menu">
                                            {files.map((file, index) => (
                                                <button
                                                    key={`${file}-${index}`}
                                                    type="button"
                                                    className={`navi-detail-file-item ${file === currFilePath ? 'active' : ''}`.trim()}
                                                    onClick={() => {
                                                        setCurrFilePath(file);
                                                        setShowFileMenu(false);
                                                    }}
                                                >
                                                    <span>{file.split(/[\\/]/).pop()}</span>
                                                    {file === currFilePath && <Check size={12} />}
                                                </button>
                                            ))}
                                        </div>
                                    )}
                                </div>

                                {showPlaybackProgress && playbackProgress !== null && (
                                    <div
                                        className="navi-detail-timeline"
                                        data-playback-state={detail.playback_state || ''}
                                        role="progressbar"
                                        aria-label="Playback progress"
                                        aria-valuemin={0}
                                        aria-valuemax={100}
                                        aria-valuenow={Math.round(playbackProgress)}
                                    >
                                        <span>{formatPlaybackTime(detail.position)}</span>
                                        <div className="navi-detail-timeline-track">
                                            <span style={{ width: `${playbackProgress}%` }} />
                                        </div>
                                        <span>{formatPlaybackTime(playbackDuration)}</span>
                                    </div>
                                )}
                            </div>

                            {metadataHint && (
                                <div className={`navi-detail-hint ${metadataPhase}`}>
                                    {metadataHint}
                                </div>
                            )}

                            <div className="navi-detail-facts">
                                <div className="navi-detail-fact">
                                    <div className="k">日期</div>
                                    <div className="v">{releaseDateLabel}</div>
                                </div>
                                <div className="navi-detail-fact">
                                    <div className="k">时长</div>
                                    <div className="v">{runtimeLabel}</div>
                                </div>
                                <div className="navi-detail-fact">
                                    <div className="k">发行</div>
                                    <div className="v">{releaseLabel}</div>
                                </div>
                                {hasFileFacts && (
                                    <>
                                        <div className="sep" aria-hidden="true" />
                                        {fileSizeLabel && (
                                            <div className="navi-detail-fact">
                                                <div className="k k--file">文件大小</div>
                                                <div className="v v--file">{fileSizeLabel}</div>
                                            </div>
                                        )}
                                        {addedDateLabel && (
                                            <div className="navi-detail-fact">
                                                <div className="k k--file">加入时间</div>
                                                <div className="v v--file">{addedDateLabel}</div>
                                            </div>
                                        )}
                                    </>
                                )}
                            </div>

                            <div className="navi-detail-attr">
                                <div className="k">演员</div>
                                <div className="navi-detail-attr-values">
                                    {actors.length > 0 ? actors.map((actor: DetailActor, index: number) => (
                                        <button
                                            type="button"
                                            key={actor.id || `${actor.name}-${index}`}
                                            className="navi-actor-pill"
                                            onClick={() => onSelectFilter({ type: 'actor', value: actor.id || actor.name, label: actor.name })}
                                        >
                                            <UserRound size={12} />
                                            {actor.name}
                                        </button>
                                    )) : <span className="navi-detail-attr-empty">未知</span>}
                                </div>
                            </div>

                            <div className="navi-detail-attr is-tags">
                                <div className="k">类型</div>
                                <div className="navi-detail-attr-values">
                                    {contentTags.length > 0 ? contentTags.map((tag: string, index: number) => (
                                        <button
                                            key={`${tag}-${index}`}
                                            type="button"
                                            className="navi-tag-pill"
                                            onClick={() => onSelectFilter({ type: 'genre', value: tag, label: tag })}
                                        >
                                            {tag}
                                        </button>
                                    )) : <span className="navi-detail-attr-empty">未分类</span>}
                                    {detailSeries?.title && !hasContentTag(`系列: ${detailSeries.title}`) && (
                                        <button
                                            type="button"
                                            className="navi-tag-pill"
                                            onClick={() => onSelectFilter({ type: 'series', value: detailSeries.id, label: detailSeries.title })}
                                        >
                                            系列: {detailSeries.title}
                                        </button>
                                    )}
                                    {makerLabel && !hasContentTag(`片商: ${makerLabel}`) && <span className="navi-tag-pill is-static">片商: {makerLabel}</span>}
                                    {studioLabel && !hasContentTag(`发行: ${studioLabel}`) && <span className="navi-tag-pill is-static">发行: {studioLabel}</span>}
                                </div>
                            </div>

                            {/* 我的标签是信息表的最后一行；为空时也要显示，否则用户不知道有这功能 */}
                            <div className="navi-detail-attr is-tags" ref={myTagsRowRef}>
                                <div className="k">我的标签</div>
                                <div className="navi-detail-attr-values">
                                    {myTags.map((tag: any) => (
                                        <span className="navi-my-tag-pill" key={tag.id}>
                                            <span
                                                className="navi-tag-dot"
                                                style={{ background: categoryColor(categoryColors, typeof tag.category === 'string' ? tag.category : '') }}
                                            />
                                            {tag.name}
                                        </span>
                                    ))}
                                    <button
                                        type="button"
                                        className="navi-my-tag-add"
                                        onClick={(event) => setTagPickerRect(event.currentTarget.getBoundingClientRect())}
                                    >
                                        <Plus size={13} />
                                        <span>标签</span>
                                    </button>
                                </div>
                            </div>

                            <div className={`navi-detail-overview ${isOverviewExpanded ? '' : 'collapsed'}`.trim()}>
                                {overviewText || getMetadataFallback(detail, '暂无简介')}
                                {isOverviewCollapsible && (
                                    <button
                                        type="button"
                                        className="navi-detail-overview-toggle"
                                        onClick={() => setIsOverviewExpanded((expanded) => !expanded)}
                                    >
                                        {isOverviewExpanded ? '收起' : '展开'}
                                    </button>
                                )}
                            </div>
                        </div>
                    </div>

                    <div className="navi-detail-wide">
                        {previewItems.length > 0 && (
                            <div className="navi-detail-block">
                                <div className="navi-detail-block-head">
                                    <span className="navi-detail-block-title">预览剧照</span>
                                    <span className="navi-detail-block-meta">{previewItems.length}</span>
                                </div>
                                <div className="navi-stills-grid">
                                    {previewItems.map((item, index) => (
                                        <button
                                            key={`${item.path}-${index}`}
                                            type="button"
                                            className={`navi-still ${item.isVideo ? 'is-trailer' : ''}`.trim()}
                                            title={item.isVideo ? '播放预告片' : undefined}
                                            onPointerEnter={item.isVideo
                                                ? () => prefetchTrailerRef.current(toLocalAssetUrl(item.path))
                                                : undefined}
                                            onClick={() => handleOpenPreviewViewer(index)}
                                        >
                                            {item.isVideo ? (
                                                <>
                                                    {trailerThumbUrl && (
                                                        <img src={trailerThumbUrl} alt="trailer" loading="lazy" />
                                                    )}
                                                    <span className="navi-still-play">
                                                        <Play size={18} fill="currentColor" />
                                                    </span>
                                                    <span className="navi-still-badge">预告片</span>
                                                </>
                                            ) : (
                                                <img src={toLocalAssetUrl(item.path)} alt="preview" loading="lazy" />
                                            )}
                                        </button>
                                    ))}
                                </div>
                            </div>
                        )}

                        {(recommendationLoading || mergedRecommendations.length > 0) && (
                            <RecommendationRail
                                title="继续看"
                                subtitle="同演员 · 同类型"
                                items={mergedRecommendations}
                                loading={recommendationLoading}
                                onSelectMedia={onSelectMedia}
                                onStatus={showMsg}
                            />
                        )}
                    </div>
                </div>

                {/* 吸顶栏放在滚动容器外面：留在里面的话宽度会被滚动条让出的 10px 截断，
                    右上角露出一条没被模糊的 fanart，看起来像断层。 */}
                <div
                    className={`navi-detail-sticky ${isStickyBarVisible ? 'visible' : ''}`.trim()}
                    onDoubleClick={WindowToggleMaximise}
                >
                    <button
                        type="button"
                        className="navi-sticky-back"
                        onClick={onClose}
                        title="返回列表"
                        aria-label="返回列表"
                    >
                        <ArrowLeft size={16} />
                    </button>

                    <div className="navi-sticky-poster" aria-hidden="true">
                        {posterUrl && <img src={posterUrl} alt="" />}
                    </div>

                    <div className="navi-sticky-copy">
                        <div className="navi-sticky-code">{mediaCode}</div>
                        <div className="navi-sticky-title" title={detail.title}>{detail.title}</div>
                    </div>

                    <div className="navi-sticky-actions">
                        <button type="button" className="navi-sticky-play" onClick={handlePlay}>
                            <Play size={14} fill="currentColor" />
                            <span>{playLabel}</span>
                        </button>
                        <button
                            type="button"
                            className={`navi-sticky-fav ${detail.is_favorite ? 'on' : ''}`.trim()}
                            onClick={handleFav}
                            title={detail.is_favorite ? '已收藏' : '收藏'}
                            aria-label={detail.is_favorite ? '已收藏' : '收藏'}
                        >
                            <Heart size={15} fill={detail.is_favorite ? 'currentColor' : 'none'} />
                        </button>
                    </div>
                </div>

                {isPreviewViewerOpen && currentPreviewPath && (
                    <div className="detail-preview-viewer" onClick={handleClosePreviewViewer}>
                        <div className="detail-preview-viewer-overlay" />
                        {hasPreviewNavigation && (
                            <button
                                type="button"
                                className="detail-preview-viewer-nav prev"
                                onClick={(event) => {
                                    event.stopPropagation();
                                    handlePreviewViewerPrev();
                                }}
                                disabled={!canViewPrevPreview}
                                aria-label="Previous preview"
                            >
                                <ChevronLeft size={18} />
                            </button>
                        )}
                        <div className="detail-preview-viewer-content">
                            <div className="detail-preview-viewer-image-shell" onClick={(event) => event.stopPropagation()}>
                                {currentPreviewItem?.isVideo ? (
                                    <video
                                        key={currentPreviewPath}
                                        ref={applyTrailerVolume}
                                        src={toLocalAssetUrl(currentPreviewPath)}
                                        className="detail-preview-viewer-video"
                                        poster={trailerThumbUrl || undefined}
                                        controls
                                        autoPlay
                                        onVolumeChange={handleTrailerVolumeChange}
                                    />
                                ) : (
                                    <img
                                        src={toLocalAssetUrl(currentPreviewPath)}
                                        className="detail-preview-viewer-image"
                                        alt="preview enlarged"
                                    />
                                )}
                            </div>
                        </div>
                        {hasPreviewNavigation && (
                            <button
                                type="button"
                                className="detail-preview-viewer-nav next"
                                onClick={(event) => {
                                    event.stopPropagation();
                                    handlePreviewViewerNext();
                                }}
                                disabled={!canViewNextPreview}
                                aria-label="Next preview"
                            >
                                <ChevronRight size={18} />
                            </button>
                        )}
                    </div>
                )}
            </div>

            {codeCopyFeedback && (
                <div
                    className="meta-copy-feedback"
                    style={{
                        left: `${codeCopyFeedback.x}px`,
                        top: `${codeCopyFeedback.y}px`,
                    }}
                >
                    {'\u5df2\u590d\u5236'}
                </div>
            )}

            {tagPickerRect && (
                <TagPicker
                    mediaIDs={[detail.id]}
                    assigned={myTagAssignment}
                    targetLabel={detail.title || detail.code || '这部影片'}
                    style={{
                        position: 'fixed',
                        left: Math.max(12, Math.min(tagPickerRect.left, window.innerWidth - 302)),
                        top: Math.min(tagPickerRect.bottom + 6, window.innerHeight - 320),
                    }}
                    onClose={() => setTagPickerRect(null)}
                    onChanged={() => {
                        invalidateUserTags();
                        // 标签变化后重新拉一次详情，my_tags 才会跟上
                        GetMediaDetail(detail.id).then((fresh: any) => {
                            if (!fresh) {
                                return;
                            }
                            const nextDetail = { ...detail, my_tags: fresh.my_tags || [] } as AppMedia;
                            setDetail(nextDetail);
                            mergeMediaDetailCacheEntry(nextDetail);
                            onMediaChange?.(nextDetail);
                        }).catch((error: unknown) => console.error(error));
                    }}
                />
            )}

            {showNFOEditor && (
                <NFOEditModal
                    data={nfoEditorData}
                    loading={nfoLoading}
                    saving={nfoSaving}
                    onClose={() => !nfoSaving && setShowNFOEditor(false)}
                    onSave={handleSaveNFO}
                />
            )}
        </>
    );
};

export default MediaDetail;
