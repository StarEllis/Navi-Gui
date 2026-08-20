import React, { useState } from 'react';
import { Star } from 'lucide-react';

interface StarRatingProps {
    value: number;
    size?: number;
    readonly?: boolean;
    // 空心星在卡片上要比详情页亮一点，否则压在海报上看不见
    emptyColor?: string;
    label?: string;
    onChange?: (score: number) => void;
}

const STARS = [1, 2, 3, 4, 5];

const StarRating: React.FC<StarRatingProps> = ({
    value,
    size = 19,
    readonly = false,
    emptyColor = 'rgba(255,255,255,.2)',
    label = '我的评分',
    onChange,
}) => {
    const [hovered, setHovered] = useState(0);
    // hover 只是预览，不落库，鼠标移开就还原
    const shown = hovered > 0 ? hovered : value;

    return (
        <div
            className={`navi-star-rating ${readonly ? 'readonly' : ''}`.trim()}
            role={readonly ? 'img' : 'radiogroup'}
            aria-label={value > 0 ? `${label} ${value} 星` : `${label} 未评分`}
            onMouseLeave={() => setHovered(0)}
        >
            {STARS.map((star) => {
                const filled = star <= shown;
                const color = filled ? '#e0a05a' : emptyColor;
                if (readonly) {
                    return (
                        <Star key={star} size={size} color={color} fill={filled ? '#e0a05a' : 'none'} />
                    );
                }
                return (
                    <button
                        key={star}
                        type="button"
                        className="navi-star-btn"
                        role="radio"
                        aria-checked={value === star}
                        // 点第 N 颗 = N 星，再点同一颗 = 清空
                        aria-label={value === star ? `清除评分` : `打 ${star} 星`}
                        onMouseEnter={() => setHovered(star)}
                        onClick={(event) => {
                            event.stopPropagation();
                            onChange?.(value === star ? 0 : star);
                        }}
                    >
                        <Star size={size} color={color} fill={filled ? '#e0a05a' : 'none'} />
                    </button>
                );
            })}
        </div>
    );
};

export default StarRating;
