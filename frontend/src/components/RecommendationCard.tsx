import React from 'react';
import { Play } from 'lucide-react';
import { PlayMedia } from "../../wailsjs/go/main/App";
import { formatError, toLocalAssetUrl } from '../utils/media';
import type { AppMedia, RecommendationItem } from '../types/wails';

interface RecommendationCardProps {
    item: RecommendationItem;
    onSelectMedia: (media: AppMedia) => void;
    onStatus?: (message: string) => void;
}

const RecommendationCard: React.FC<RecommendationCardProps> = ({ item, onSelectMedia, onStatus }) => {
    const media = item.media;
    const posterPath = typeof media?.poster_path === 'string' && media.poster_path.trim()
        ? media.poster_path.trim()
        : typeof media?.backdrop_path === 'string' && media.backdrop_path.trim()
            ? media.backdrop_path.trim()
            : '';
    const coverUrl = posterPath ? toLocalAssetUrl(posterPath) : '';

    const handleQuickPlay = async (event: React.MouseEvent<HTMLButtonElement>) => {
        event.stopPropagation();

        const targetPath = typeof media?.file_path === 'string' ? media.file_path.trim() : '';
        if (!targetPath) {
            onStatus?.('播放失败：当前推荐没有可播放文件');
            return;
        }

        try {
            onStatus?.(`正在启动播放器：${targetPath.split(/[\\/]/).pop()}`);
            await PlayMedia(media.id, targetPath);
        } catch (error) {
            console.error(error);
            onStatus?.(`播放失败：${formatError(error)}`);
        }
    };

    const handleOpenDetail = () => {
        onSelectMedia(media);
    };

    const handleKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
        if (event.key === 'Enter' || event.key === ' ') {
            event.preventDefault();
            handleOpenDetail();
        }
    };

    return (
        <div
            className="recommendation-card"
            role="button"
            tabIndex={0}
            onClick={handleOpenDetail}
            onKeyDown={handleKeyDown}
        >
            <div className="recommendation-card-media">
                <span className={`recommendation-reason recommendation-reason-floating recommendation-reason-${item.match_type || 'default'}`}>
                    {item.reason || '相关推荐'}
                </span>
                {coverUrl ? (
                    <img
                        src={coverUrl}
                        className="recommendation-card-image"
                        alt={media.title || '推荐内容'}
                        loading="lazy"
                    />
                ) : (
                    <div className="recommendation-card-image recommendation-card-image-empty">
                        No Image
                    </div>
                )}
                <div className="recommendation-card-overlay">
                    <button
                        type="button"
                        className="recommendation-card-play"
                        onClick={handleQuickPlay}
                        aria-label={`播放 ${media.title || '推荐内容'}`}
                    >
                        <Play size={16} fill="currentColor" />
                    </button>
                </div>
            </div>
            <div className="recommendation-card-copy">
                <div className="recommendation-title" title={media.title || ''}>
                    {media.title || '未命名'}
                </div>
                <div className="recommendation-meta">
                    <span>{media.release_date_normalized || media.year || '未知日期'}</span>
                    {media.code_prefix && <span>{media.code_prefix}</span>}
                </div>
            </div>
        </div>
    );
};

export default RecommendationCard;
