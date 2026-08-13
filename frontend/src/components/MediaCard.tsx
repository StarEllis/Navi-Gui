import React, { useMemo } from 'react';
import { FolderOpen, Heart, Play } from 'lucide-react';
import { OpenMediaFolder, PlayMedia, ToggleFavorite } from "../../wailsjs/go/main/App";
import { formatMediaMeta, toLocalAssetUrl } from '../utils/media';
import { areMediaCardMediaPropsEqual, shouldOpenMediaFromCardKey } from '../utils/mediaCardState';
import { markComponentRender } from '../utils/performanceDiagnostics';
import { formatPlaybackTime, getMediaProgressPercent } from '../utils/mediaPlaybackState';
import type { StatusKind } from '../types/status';

interface MediaCardProps {
    media: any;
    onSelectMedia: (media: any) => void;
    onQuickPlayStatus?: (message: string, kind?: StatusKind) => void;
    onPrefetchMedia?: (media: any) => void;
    onFocusMedia?: (mediaId: string) => void;
    onMediaChange?: (media: any) => void;
}

// 与 service/player/manager.go 的续播阈值一致：进度过了这条线就当看完了。
const RESUME_PROGRESS_LIMIT = 90;

const formatError = (error: unknown) => {
    if (error instanceof Error && error.message) {
        return error.message;
    }
    if (typeof error === 'string') {
        return error;
    }
    return '未知错误';
};

const MediaCard: React.FC<MediaCardProps> = ({
    media,
    onSelectMedia,
    onQuickPlayStatus,
    onPrefetchMedia,
    onFocusMedia,
    onMediaChange,
}) => {
    markComponentRender('MediaCard');
    const coverUrl = useMemo(() => (
        media.poster_path
            ? toLocalAssetUrl(media.poster_path)
            : media.backdrop_path
                ? toLocalAssetUrl(media.backdrop_path)
                : ''
    ), [media.backdrop_path, media.poster_path]);
    const playbackProgress = getMediaProgressPercent(media) ?? 0;
    const hasProgressBar = playbackProgress > 0;
    const title = media.title || '未知标题';

    // 播放位置 / 总时长：与 getMediaProgressPercent 一致，watch_duration 优先。
    const position = typeof media.position === 'number' && media.position > 0 ? media.position : 0;
    const watchDuration = typeof media.watch_duration === 'number' && media.watch_duration > 0 ? media.watch_duration : 0;
    const duration = watchDuration || (typeof media.duration === 'number' && media.duration > 0 ? media.duration : 0);
    // 「看了一半」：hover 时给继续播放，副标题换成剩余时长。
    // 90% 这个界跟后端对齐：service/player/manager.go 里超过 90% 就不再做续播跳转，
    // 前端要是还写「从 xx 继续」，点下去会从头播。
    const hasResumePoint = hasProgressBar
        && playbackProgress < RESUME_PROGRESS_LIMIT
        && position > 0;
    const remainingSeconds = hasResumePoint && duration > position ? duration - position : 0;
    const isWatched = media.is_watched === true && !hasResumePoint;

    const resolution = typeof media.resolution === 'string' ? media.resolution.trim() : '';
    const metaLine = remainingSeconds > 0
        ? [`剩 ${formatPlaybackTime(remainingSeconds)}`, resolution].filter(Boolean).join(' · ')
        : formatMediaMeta(media);

    const handleQuickPlay = async (event: React.MouseEvent<HTMLButtonElement>) => {
        event.stopPropagation();
        const targetPath = typeof media?.file_path === 'string' ? media.file_path.trim() : '';
        if (!targetPath) {
            onQuickPlayStatus?.('播放失败：当前卡片没有可播放文件', 'error');
            return;
        }

        try {
            onQuickPlayStatus?.(`正在启动播放器：${targetPath.split(/[\\/]/).pop()}`, 'play');
            await PlayMedia(media.id, targetPath);
        } catch (error) {
            console.error(error);
            onQuickPlayStatus?.(`播放失败：${formatError(error)}`, 'error');
        }
    };

    const handleToggleFavorite = async (event: React.MouseEvent<HTMLButtonElement>) => {
        event.stopPropagation();
        try {
            await ToggleFavorite(media.id);
            onMediaChange?.({ ...media, is_favorite: !media.is_favorite });
        } catch (error) {
            console.error(error);
            onQuickPlayStatus?.(`收藏失败：${formatError(error)}`, 'error');
        }
    };

    const handleOpenFolder = async (event: React.MouseEvent<HTMLButtonElement>) => {
        event.stopPropagation();
        try {
            await OpenMediaFolder(media.id);
        } catch (error) {
            console.error(error);
            onQuickPlayStatus?.(`打开目录失败：${formatError(error)}`, 'error');
        }
    };

    const openMedia = () => onSelectMedia(media);

    return (
        <div
            className={`navi-card ${isWatched ? 'watched' : ''}`.trim()}
            role="button"
            tabIndex={0}
            aria-label={`打开 ${title} 详情`}
            onClick={openMedia}
            onKeyDown={(event) => {
                if (shouldOpenMediaFromCardKey(event.key, event.target === event.currentTarget)) {
                    event.preventDefault();
                    openMedia();
                }
            }}
            onPointerEnter={() => onPrefetchMedia?.(media)}
            onFocus={() => {
                onFocusMedia?.(media.id);
                onPrefetchMedia?.(media);
            }}
        >
            <div className="navi-card-poster">
                {coverUrl && (
                    <img
                        key={coverUrl}
                        src={coverUrl}
                        className="navi-card-image"
                        alt={title}
                        loading="lazy"
                        decoding="async"
                        width={192}
                        height={288}
                        onError={(event) => {
                            event.currentTarget.hidden = true;
                        }}
                    />
                )}

                <div className={`navi-card-hover ${hasResumePoint ? 'has-resume' : ''}`.trim()}>
                    <div className="navi-card-corner">
                        <button
                            type="button"
                            className={`navi-card-action ${media.is_favorite ? 'on' : ''}`.trim()}
                            title={media.is_favorite ? '取消收藏' : '收藏'}
                            aria-label={media.is_favorite ? '取消收藏' : '收藏'}
                            onClick={handleToggleFavorite}
                        >
                            <Heart size={13} fill={media.is_favorite ? 'currentColor' : 'none'} />
                        </button>
                        <button
                            type="button"
                            className="navi-card-action"
                            title="打开所在目录"
                            aria-label="打开所在目录"
                            onClick={handleOpenFolder}
                        >
                            <FolderOpen size={13} />
                        </button>
                    </div>

                    <button
                        type="button"
                        className="navi-card-play"
                        aria-label={hasResumePoint ? `继续播放 ${title}` : `播放 ${title}`}
                        onClick={handleQuickPlay}
                    >
                        <Play size={19} fill="currentColor" />
                    </button>

                    {hasResumePoint && (
                        <div className="navi-card-resume">从 {formatPlaybackTime(position)} 继续</div>
                    )}
                </div>

                {/* 收边渐变只为进度条垫底：没看过的卡片海报保持完全干净 */}
                {hasProgressBar && (
                    <>
                        <div className="navi-card-scrim" />
                        <div
                            className="navi-card-progress"
                            role="progressbar"
                            aria-label="Playback progress"
                            aria-valuemin={0}
                            aria-valuemax={100}
                            aria-valuenow={Math.round(playbackProgress)}
                        >
                            <span style={{ width: `${playbackProgress}%` }} />
                        </div>
                    </>
                )}
            </div>

            <div className="navi-card-title" title={title}>{title}</div>
            <div className="navi-card-meta">{metaLine || '未知日期'}</div>
        </div>
    );
};

export default React.memo(MediaCard, (prev, next) => (
    areMediaCardMediaPropsEqual(prev.media, next.media)
    && prev.onSelectMedia === next.onSelectMedia
    && prev.onQuickPlayStatus === next.onQuickPlayStatus
    && prev.onPrefetchMedia === next.onPrefetchMedia
    && prev.onFocusMedia === next.onFocusMedia
    && prev.onMediaChange === next.onMediaChange
));
