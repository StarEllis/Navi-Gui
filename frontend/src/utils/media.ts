export const formatError = (error: unknown) => {
    if (error instanceof Error && error.message) {
        return error.message;
    }
    if (typeof error === 'string') {
        return error;
    }
    return '未知错误';
};

export const toLocalAssetUrl = (path: string) => `/local/${encodeURIComponent(path)}`;

/** 卡片副标题：年份 · 画质 · 时长 */
export const formatMediaMeta = (media: Record<string, any> | null | undefined) => {
    const parts: string[] = [];

    const year = media?.year;
    if (typeof year === 'number' && year > 0) {
        parts.push(String(year));
    }

    const resolution = typeof media?.resolution === 'string' ? media.resolution.trim() : '';
    if (resolution) {
        parts.push(resolution);
    }

    const durationSeconds = typeof media?.duration === 'number' ? media.duration : 0;
    const runtimeMinutes = durationSeconds > 0
        ? Math.round(durationSeconds / 60)
        : (typeof media?.runtime === 'number' ? media.runtime : 0);
    if (runtimeMinutes > 0) {
        parts.push(`${runtimeMinutes} min`);
    }

    return parts.join(' · ');
};
