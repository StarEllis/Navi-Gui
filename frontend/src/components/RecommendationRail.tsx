import React from 'react';
import RecommendationCard from './RecommendationCard';
import type { AppMedia, RecommendationItem } from '../types/wails';
import type { StatusKind } from '../types/status';

interface RecommendationRailProps {
    title: string;
    subtitle?: string;
    items: RecommendationItem[];
    loading?: boolean;
    onSelectMedia: (media: AppMedia) => void;
    onStatus?: (message: string, kind?: StatusKind) => void;
}

const RecommendationRail: React.FC<RecommendationRailProps> = ({
    title,
    subtitle,
    items,
    loading = false,
    onSelectMedia,
    onStatus,
}) => {
    const handleWheel = (event: React.WheelEvent<HTMLDivElement>) => {
        const container = event.currentTarget;
        if (container.scrollWidth <= container.clientWidth) {
            return;
        }

        const delta = Math.abs(event.deltaX) > Math.abs(event.deltaY) ? event.deltaX : event.deltaY;
        if (delta === 0) {
            return;
        }

        container.scrollLeft += delta;
        event.preventDefault();
    };

    if (!loading && (!Array.isArray(items) || items.length === 0)) {
        return null;
    }

    return (
        <section className="recommendation-rail-section">
            <div className="recommendation-rail-header">
                <div>
                    <h3 className="recommendation-rail-title">{title}</h3>
                    {subtitle && <p className="recommendation-rail-subtitle">{subtitle}</p>}
                </div>
                {loading && <span className="recommendation-rail-loading">加载中</span>}
            </div>

            <div className="recommendation-rail" onWheel={handleWheel}>
                {loading
                    ? Array.from({ length: 4 }).map((_, index) => (
                        <div key={`skeleton-${index}`} className="recommendation-card recommendation-card-skeleton" />
                    ))
                    : items.map((item, index) => (
                        <RecommendationCard
                            key={`${item.media?.id || 'recommendation'}-${item.match_type || 'item'}-${index}`}
                            item={item}
                            onSelectMedia={onSelectMedia}
                            onStatus={onStatus}
                        />
                    ))}
            </div>
        </section>
    );
};

export default RecommendationRail;
