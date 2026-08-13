export const DEFAULT_LIBRARY_VIEW_MODE = 'poster';
export const DEFAULT_LIBRARY_TITLE_FIELD = 'title';
export const DEFAULT_LIBRARY_SUBTITLE_FIELD = 'year';

const normalizeFolderPaths = (paths: string[]) => {
    const seen = new Set<string>();
    return paths
        .map((path) => (typeof path === 'string' ? path.trim() : ''))
        .filter((path) => {
            if (!path || seen.has(path)) {
                return false;
            }
            seen.add(path);
            return true;
        });
};

const parseStoredPathField = (pathValue: unknown) => {
    const defaults = {
        folderPaths: [] as string[],
        viewMode: DEFAULT_LIBRARY_VIEW_MODE,
        titleField: DEFAULT_LIBRARY_TITLE_FIELD,
        subtitleField: DEFAULT_LIBRARY_SUBTITLE_FIELD,
    };

    if (typeof pathValue !== 'string') {
        return defaults;
    }

    const trimmed = pathValue.trim();
    if (!trimmed) {
        return defaults;
    }

    try {
        if (trimmed.startsWith('{')) {
            const parsed = JSON.parse(trimmed);
            return {
                folderPaths: normalizeFolderPaths(Array.isArray(parsed?.paths) ? parsed.paths : []),
                viewMode: parsed?.view_mode || defaults.viewMode,
                titleField: parsed?.title_field || defaults.titleField,
                subtitleField: parsed?.subtitle_field || defaults.subtitleField,
            };
        }
        if (trimmed.startsWith('[')) {
            const parsed = JSON.parse(trimmed);
            return {
                ...defaults,
                folderPaths: normalizeFolderPaths(Array.isArray(parsed) ? parsed : []),
            };
        }
    } catch (_error) {
        return {
            ...defaults,
            folderPaths: normalizeFolderPaths([trimmed]),
        };
    }

    return {
        ...defaults,
        folderPaths: normalizeFolderPaths([trimmed]),
    };
};

export const getLibraryConfig = (library: any) => {
    const stored = parseStoredPathField(library?.path);
    const explicitPaths = normalizeFolderPaths(Array.isArray(library?.folder_paths) ? library.folder_paths : []);

    return {
        folderPaths: explicitPaths.length > 0 ? explicitPaths : stored.folderPaths,
        viewMode: library?.view_mode || stored.viewMode,
        titleField: library?.title_field || stored.titleField,
        subtitleField: library?.subtitle_field || stored.subtitleField,
    };
};

export const buildLibraryPayload = (library: any, updates: {
    name: string;
    folderPaths: string[];
    viewMode: string;
    titleField: string;
    subtitleField: string;
}) => ({
    ...library,
    name: updates.name,
    path: updates.folderPaths[0] || '',
    folder_paths: normalizeFolderPaths(updates.folderPaths),
    view_mode: updates.viewMode || DEFAULT_LIBRARY_VIEW_MODE,
    title_field: updates.titleField || DEFAULT_LIBRARY_TITLE_FIELD,
    subtitle_field: updates.subtitleField || DEFAULT_LIBRARY_SUBTITLE_FIELD,
});

export const formatLibrarySize = (bytes: unknown) => {
    const value = typeof bytes === 'number' && Number.isFinite(bytes) ? bytes : 0;
    if (value <= 0) {
        return '';
    }

    const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
    let scaled = value;
    let unitIndex = 0;
    while (scaled >= 1024 && unitIndex < units.length - 1) {
        scaled /= 1024;
        unitIndex += 1;
    }

    const digits = unitIndex >= 3 && scaled < 100 ? 1 : 0;
    return `${scaled.toFixed(digits)} ${units[unitIndex]}`;
};

export const formatLastScanLabel = (lastScan: unknown, now: number = Date.now()) => {
    if (typeof lastScan !== 'string' || !lastScan.trim() || lastScan.startsWith('0001-01-01')) {
        return '';
    }

    const scannedAt = new Date(lastScan).getTime();
    if (!Number.isFinite(scannedAt)) {
        return '';
    }

    const minutes = Math.floor((now - scannedAt) / 60000);
    if (minutes < 1) {
        return '刚刚扫描';
    }
    if (minutes < 60) {
        return `${minutes} 分钟前扫描`;
    }

    const hours = Math.floor(minutes / 60);
    if (hours < 24) {
        return `${hours} 小时前扫描`;
    }

    const days = Math.floor(hours / 24);
    if (days < 30) {
        return `${days} 天前扫描`;
    }

    return `${new Date(scannedAt).toLocaleDateString('zh-CN')} 扫描`;
};

export const getSortLabel = (field: string) => {
    switch (field) {
        case 'release_date':
            return '发行日期';
        case 'video_codec':
            return '视频编码';
        case 'last_watched':
            return '最近观看';
        case 'created_at':
        default:
            return '加入日期';
    }
};
