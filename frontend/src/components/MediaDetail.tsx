import React, { useEffect, useRef, useState } from 'react';
import {
    DeleteMedia,
    GetDetailRecommendations,
    GetMediaDetail,
    GetNFOEditorData,
    OpenMediaFolder,
    PlayMedia,
    SaveNFOEditorData,
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
    Eye,
    EyeOff,
    FileEdit,
    FolderOpen,
    Play,
    Star,
    Trash2,
} from 'lucide-react';
import NFOEditModal from './NFOEditModal';
import RecommendationRail from './RecommendationRail';
import type {
    AppMedia,
    MediaFilter,
    NFOEditorDraft,
    RecommendationGroups,
    RecommendationItem,
} from '../types/wails';
import { formatError, toLocalAssetUrl } from '../utils/media';
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
    onClose: () => void;
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

const normalizeTags = (detail: AppMedia) => {
    const rawTags = detail.genres
        ? String(detail.genres).split(/[,，/]/).map((tag: string) => tag.trim()).filter(Boolean)
        : [];

    const technicalKeywords = [
        '4K', '1080P', '720P', 'UHD', 'HD', 'FHD', 'SD',
        'H265', 'HEVC', 'H264', 'X264', 'X265', 'AV1', 'HDR',
        '中文字幕', '字幕', '60FPS', 'FPS', '无码', '流出', 'REMUX', 'WEB-DL',
    ];
    const coreKeywords = [
        '剧情', '恋爱', '人妻', '素人', '学生', '老师', '护士', '秘书', 'OL',
        '校园', '职场', '旅行', '温泉', '家庭', '情侣', '制服', '巨乳',
        '熟女', '姐姐', '妹妹', '偶像', '角色', '人物',
    ];

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
        const upper = tag.toUpperCase();
        const isTechnical = technicalKeywords.some((keyword) => upper.includes(keyword) || tag.includes(keyword));
        if (isTechnical) {
            return 200 + tag.length;
        }
        const isCore = coreKeywords.some((keyword) => tag.includes(keyword));
        if (isCore) {
            return tag.length;
        }
        return 80 + tag.length;
    };

    return uniqueTags.sort((left: string, right: string) => scoreTag(left) - scoreTag(right));
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
    onClose,
    onSelectMedia,
    onSelectFilter,
    onMediaChange,
    onMediaDelete,
}) => {
    const [detail, setDetail] = useState(media);
    const [msg, setMsg] = useState('');
    const [files, setFiles] = useState<string[]>([]);
    const [previews, setPreviews] = useState<string[]>([]);
    const [recommendations, setRecommendations] = useState<RecommendationGroups>(emptyRecommendations);
    const [recommendationLoading, setRecommendationLoading] = useState(false);
    const [currFilePath, setCurrFilePath] = useState(media.file_path || '');
    const [showFileMenu, setShowFileMenu] = useState(false);
    const [showNFOEditor, setShowNFOEditor] = useState(false);
    const [nfoEditorData, setNfoEditorData] = useState<NFOEditorDraft | null>(null);
    const [nfoLoading, setNfoLoading] = useState(false);
    const [nfoSaving, setNfoSaving] = useState(false);
    const [previewViewerIndex, setPreviewViewerIndex] = useState<number | null>(null);
    const [codeCopyFeedback, setCodeCopyFeedback] = useState<CopyFeedback | null>(null);
    const fileDropdownRef = useRef<HTMLDivElement | null>(null);
    const chipRowRef = useRef<HTMLDivElement | null>(null);
    const chipStripRef = useRef<HTMLDivElement | null>(null);
    const previewStripRef = useRef<HTMLDivElement | null>(null);
    const codeCopyTimerRef = useRef<number | null>(null);
    const mediaStateByIDRef = useRef(new Map<string, MediaStateUpdate>());

    const showMsg = (message: string) => {
        setMsg(message);
        window.setTimeout(() => setMsg(''), 4000);
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
        const initialDetail = cachedEntry?.detail || media;
        const knownInitialState = mediaStateByIDRef.current.get(media.id);
        const resolvedInitialDetail = knownInitialState
            ? applyMediaStateUpdate(initialDetail as AppMedia & Record<string, any>, knownInitialState) as AppMedia
            : initialDetail;

        seedMediaDetailCache(media);
        setDetail(resolvedInitialDetail);
        setFiles(fallbackFiles);
        setPreviews(fallbackPreviews);
        setCurrFilePath((cachedEntry?.detail?.file_path || fallbackFiles[0] || media.file_path || '').trim());
        setRecommendations(emptyRecommendations);
        setRecommendationLoading(true);
        setCodeCopyFeedback(null);
        clearCodeCopyFeedbackTimer();

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
                setCurrFilePath((resolvedDetail?.file_path || nextEntry.files[0] || media.file_path || '').trim());
                onMediaChange?.(resolvedDetail);
            } catch (error) {
                console.error(error);
                if (!active) {
                    return;
                }
                showMsg(`加载详情失败：${formatError(error)}`);
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
                    if (currentIndex === null || currentIndex >= previews.length - 1) {
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
    }, [previewViewerIndex, previews.length]);

    useEffect(() => {
        if (previewViewerIndex !== null && previewViewerIndex >= previews.length) {
            setPreviewViewerIndex(previews.length > 0 ? previews.length - 1 : null);
        }
    }, [previewViewerIndex, previews.length]);

    useEffect(() => {
        const chipRow = chipRowRef.current;
        if (!chipRow) {
            return;
        }

        const handleWheel = (event: WheelEvent) => {
            if (!scrollHorizontalContainer(chipStripRef.current, event.deltaX, event.deltaY)) {
                return;
            }

            event.preventDefault();
            event.stopPropagation();
        };

        chipRow.addEventListener('wheel', handleWheel, { passive: false, capture: true });
        return () => {
            chipRow.removeEventListener('wheel', handleWheel, true);
        };
    }, []);

    useEffect(() => {
        const previewStrip = previewStripRef.current;
        if (!previewStrip) {
            return;
        }

        const handleWheel = (event: WheelEvent) => {
            if (!scrollHorizontalContainer(previewStrip, event.deltaX, event.deltaY)) {
                return;
            }

            event.preventDefault();
            event.stopPropagation();
        };

        previewStrip.addEventListener('wheel', handleWheel, { passive: false, capture: true });
        return () => {
            previewStrip.removeEventListener('wheel', handleWheel, true);
        };
    }, [previews.length]);

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
            showMsg(`加载推荐失败：${formatError(error)}`);
        } finally {
            setRecommendationLoading(false);
        }
    };

    const handlePlay = async () => {
        const targetPath = (currFilePath || detail.file_path || media.file_path || '').trim();
        if (!targetPath) {
            showMsg('播放失败：当前没有可播放文件');
            return;
        }

        try {
            showMsg(`正在启动播放器：${targetPath.split(/[\\/]/).pop()}`);
            await PlayMedia(detail.id, targetPath);
            await refreshDetail();
        } catch (error) {
            console.error(error);
            showMsg(`播放失败：${formatError(error)}`);
        }
    };

    const handleOpenDir = async () => {
        try {
            await OpenMediaFolder(detail.id);
        } catch (error) {
            console.error(error);
            showMsg(`打开目录失败：${formatError(error)}`);
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
            showMsg(`打开 NFO 失败：${formatError(error)}`);
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
                showMsg('为避免数据丢失，此 NFO 结构暂不支持编辑');
            } else {
                showMsg(`保存 NFO 失败：${message}`);
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
            showMsg(`删除失败：${formatError(error)}`);
        }
    };

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
            showMsg(`收藏失败：${formatError(error)}`);
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
            showMsg(`更新观看状态失败：${formatError(error)}`);
        }
    };

    const scrollHorizontalContainer = (
        container: HTMLDivElement | null,
        deltaX: number,
        deltaY: number,
    ) => {
        if (!container) {
            return false;
        }

        if (container.scrollWidth <= container.clientWidth) {
            return false;
        }

        const delta = Math.abs(deltaX) > Math.abs(deltaY) ? deltaX : deltaY;
        if (delta === 0) {
            return false;
        }

        container.scrollLeft += delta;
        return true;
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
            if (currentIndex === null || currentIndex >= previews.length - 1) {
                return currentIndex;
            }
            return currentIndex + 1;
        });
    };

    const handleCopyMediaCode = async (event: React.MouseEvent<HTMLButtonElement>) => {
        const code = getMediaCode(detail, currFilePath).trim();
        if (!code) {
            return;
        }

        try {
            const copied = await ClipboardSetText(code);
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
    const backdropUrl = backdropPath ? toLocalAssetUrl(backdropPath) : '';
    const actors = normalizeActors(detail);
    const tags = normalizeTags(detail);
    const filename = currFilePath?.split(/[\\/]/).pop() || '未知文件';
    const mediaCode = getMediaCode(detail, currFilePath);
    const detailSeries = detail.series;
    const metadataPhase = normalizeMetadataPhase(detail.metadata_phase);
    const metadataHint = metadataPhase === 'quick'
        ? '正在后台补全时长、演员和技术信息…'
        : metadataPhase === 'failed'
            ? '元数据补全失败，可重新扫描后再试。'
            : '';
    const overviewText = cleanOverview(detail.overview);
    const isPreviewViewerOpen = previewViewerIndex !== null;
    const currentPreviewPath = previewViewerIndex !== null ? previews[previewViewerIndex] : '';
    const hasPreviewNavigation = previews.length > 1;
    const canViewPrevPreview = previewViewerIndex !== null && previewViewerIndex > 0;
    const canViewNextPreview = previewViewerIndex !== null && previewViewerIndex < previews.length - 1;
    const mergedRecommendations = mergeRecommendationItems(recommendations);
    const playbackProgress = getMediaProgressPercent(detail);
    const playbackDuration = detail.watch_duration || detail.duration;
    const showPlaybackProgress = playbackProgress !== null
        && (playbackProgress > 0 || typeof detail.revision === 'number');

    return (
        <>
            <div className="detail-workspace">
                <div className="detail-backdrop-layer" aria-hidden="true">
                    {backdropUrl && (
                        <>
                            <div
                                className="detail-backdrop-image"
                                style={{ backgroundImage: `url("${backdropUrl}")` }}
                            />
                            <div className="detail-backdrop-overlay" />
                        </>
                    )}
                </div>

                <div className="detail-drag-zone" onDoubleClick={WindowToggleMaximise} />

                <div className="detail-main">
                    <div className="detail-hero">
                        <div className="detail-poster-section">
                            {posterUrl ? (
                                <img src={posterUrl} className="detail-poster" alt="poster" />
                            ) : (
                                <div className="detail-poster no-poster">No Poster</div>
                            )}
                        </div>

                        <div className="detail-info-section">
                            <div className="detail-info-surface">
                        <div className="detail-header-row">
                            <div className="detail-title">{detail.title}</div>
                            {msg && <span className="detail-status-msg">{msg}</span>}
                        </div>

                        <div className="detail-toolbar">
                            <button className="toolbar-btn danger" title="仅从数据库移除这条记录，不删除本地文件" onClick={handleDelete}><Trash2 size={16} /></button>
                            <button className="toolbar-btn" title="打开文件所在目录" onClick={handleOpenDir}><FolderOpen size={16} /></button>
                            <button className="toolbar-btn" title="编辑 NFO" onClick={handleOpenNFO}><FileEdit size={16} /></button>
                            <button className="toolbar-btn primary" title="播放当前选中文件" onClick={handlePlay}><Play size={16} fill="currentColor" /></button>
                            <button className="toolbar-btn" title="返回列表" onClick={onClose}><ArrowLeft size={16} /></button>
                            <div className="toolbar-divider" />
                            <button className="toolbar-btn" title={detail.is_favorite ? '取消收藏' : '收藏'} onClick={handleFav}>
                                <Star size={16} fill={detail.is_favorite ? "var(--accent)" : "none"} color={detail.is_favorite ? "var(--accent)" : "currentColor"} />
                            </button>
                            <button className="toolbar-btn" title={detail.is_watched ? '标记未看' : '标记已看'} onClick={handleWatched}>
                                {detail.is_watched ? <EyeOff size={16} /> : <Eye size={16} />}
                            </button>
                        </div>

                        <div className="detail-file-row">
                            <div className="detail-file-dropdown" ref={fileDropdownRef}>
                                <button
                                    type="button"
                                    className={`detail-file-select ${showFileMenu ? 'open' : ''}`}
                                    onClick={() => setShowFileMenu((open) => !open)}
                                >
                                    <span className="file-active-name" title={currFilePath}>{filename}</span>
                                    <ChevronDown size={14} className={`chevron ${showFileMenu ? 'open' : ''}`} />
                                </button>

                                {showFileMenu && (
                                    <div className="file-dropdown-menu">
                                        {files.map((file, index) => (
                                            <button
                                                key={`${file}-${index}`}
                                                type="button"
                                                className={`file-menu-item ${file === currFilePath ? 'active' : ''}`}
                                                onClick={() => {
                                                    setCurrFilePath(file);
                                                    setShowFileMenu(false);
                                                }}
                                            >
                                                <span className="file-item-name">{file.split(/[\\/]/).pop()}</span>
                                                {file === currFilePath && <Check size={12} color="var(--accent)" />}
                                            </button>
                                        ))}
                                    </div>
                                )}
                            </div>
                        </div>

                        {showPlaybackProgress && (
                            <div className="detail-playback-progress" data-playback-state={detail.playback_state || ''}>
                                <div className="detail-playback-progress-copy">
                                    <span>{formatPlaybackTime(detail.position)} / {formatPlaybackTime(playbackDuration)}</span>
                                    <span>{Math.round(playbackProgress)}%</span>
                                </div>
                                <div
                                    className="playback-progress-track detail-playback-progress-track"
                                    role="progressbar"
                                    aria-label="Playback progress"
                                    aria-valuemin={0}
                                    aria-valuemax={100}
                                    aria-valuenow={Math.round(playbackProgress)}
                                >
                                    <span className="playback-progress-value" style={{ width: `${playbackProgress}%` }} />
                                </div>
                            </div>
                        )}

                        {metadataHint && (
                            <div className={`detail-metadata-hint ${metadataPhase}`}>
                                {metadataHint}
                            </div>
                        )}

                        <div className="detail-meta-grid">
                            <div className="meta-row">
                                <span className="meta-label">编号</span>
                                <div className="meta-value meta-copy-wrap">
                                    <button
                                        type="button"
                                        className="meta-copy-button highlight"
                                        onClick={handleCopyMediaCode}
                                        aria-label={`Copy code ${mediaCode}`}
                                        title={'\u70b9\u51fb\u590d\u5236'}
                                    >
                                        {mediaCode}
                                    </button>
                                </div>
                            </div>
                            <div className="meta-row">
                                <span className="meta-label">日期</span>
                                <span className="meta-value">{detail.release_date_normalized || detail.year || '未知'}</span>
                            </div>
                            <div className="meta-row">
                                <span className="meta-label">时长</span>
                                <span className="meta-value">
                                    {detail.duration ? `${Math.floor(detail.duration / 60)} min` : (detail.runtime ? `${detail.runtime} min` : '未知')}
                                </span>
                            </div>
                            <div className="meta-row chips-row">
                                <span className="meta-label">演员</span>
                                <div className="meta-value actor-chip-strip">
                                    {actors.length > 0 ? actors.map((actor: DetailActor, index: number) => (
                                        <button
                                            type="button"
                                            key={actor.id || `${actor.name}-${index}`}
                                            className="meta-pill-chip actor-pill-chip"
                                            onClick={() => onSelectFilter({ type: 'actor', value: actor.id || actor.name, label: actor.name })}
                                        >
                                            {actor.name}
                                        </button>
                                    )) : '未知'}
                                </div>
                            </div>
                            <div className="meta-row chips-row" ref={chipRowRef}>
                                <span className="meta-label">类型</span>
                                <div
                                    className="meta-value chip-strip-shell"
                                    ref={chipStripRef}
                                >
                                    <div className="tag-chips-scroll">
                                        {tags.length > 0 ? tags.map((tag: string, index: number) => (
                                            <button
                                                key={`${tag}-${index}`}
                                                type="button"
                                                className="meta-pill-chip"
                                                onClick={() => onSelectFilter({ type: 'genre', value: tag, label: tag })}
                                            >
                                                {tag}
                                            </button>
                                        )) : '未分类'}
                                    </div>
                                </div>
                            </div>
                            {detailSeries?.title && (
                                <div className="meta-row">
                                    <span className="meta-label">系列</span>
                                    <span
                                        className="meta-value meta-item-clickable"
                                        onClick={() => onSelectFilter({ type: 'series', value: detailSeries.id, label: detailSeries.title })}
                                    >
                                        {detailSeries.title}
                                    </span>
                                </div>
                            )}
                            {(detail.studio || detail.publisher) && (
                                <div className="meta-row">
                                    <span className="meta-label">发行</span>
                                    <span className="meta-value">{detail.studio || detail.publisher}</span>
                                </div>
                            )}
                        </div>

                        <div className="detail-desc">
                            {overviewText || getMetadataFallback(detail, '暂无简介')}
                        </div>

                        {previews.length > 0 && (
                            <div className="detail-previews-container">
                                <div className="previews-label">预览剧照 ({previews.length})</div>
                                <div
                                    className={`preview-strip ${previews.length > 1 ? 'has-scrollbar' : 'no-scrollbar'}`}
                                    ref={previewStripRef}
                                >
                                    {previews.map((preview, index) => (
                                        <button
                                            key={`${preview}-${index}`}
                                            type="button"
                                            className="preview-item"
                                            onClick={() => handleOpenPreviewViewer(index)}
                                        >
                                            <img src={toLocalAssetUrl(preview)} className="preview-img" alt="preview" loading="lazy" />
                                        </button>
                                    ))}
                                </div>
                            </div>
                        )}
                            </div>
                        </div>
                    </div>
                    {(recommendationLoading || mergedRecommendations.length > 0) && (
                        <div className="detail-recommendation-block">
                            <RecommendationRail
                                title="继续看"
                                items={mergedRecommendations}
                                loading={recommendationLoading}
                                onSelectMedia={onSelectMedia}
                                onStatus={showMsg}
                            />
                        </div>
                    )}
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
                                <img
                                    src={toLocalAssetUrl(currentPreviewPath)}
                                    className="detail-preview-viewer-image"
                                    alt="preview enlarged"
                                />
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
