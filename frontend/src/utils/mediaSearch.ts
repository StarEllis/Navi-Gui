const collapseWhitespace = (value: string) => value.replace(/\s+/g, ' ').trim();

export const normalizeSearchTerm = (value: string) => collapseWhitespace(
    value
        .normalize('NFKC')
        .toLowerCase()
        .replace(/[\u0000-\u001f]+/g, ' ')
        .replace(/[_\-./\\[\](){}#+]+/g, ' '),
);

export const normalizeSearchField = (value: unknown) => {
    if (typeof value === 'number') {
        return normalizeSearchTerm(String(value));
    }

    if (typeof value !== 'string') {
        return '';
    }

    const trimmedValue = value.trim();
    if (!trimmedValue) {
        return '';
    }

    return normalizeSearchTerm(trimmedValue);
};

export const buildMediaSearchText = (media: any) => {
    if (!media || typeof media !== 'object') {
        return '';
    }

    const parts = [
        typeof media.search_text === 'string' ? media.search_text : '',
        media.title,
        media.orig_title,
        media.code,
        media.actor,
        media.genres,
        media.studio,
        media.maker,
        media.label,
        media.release_date_normalized,
        media.file_path,
        typeof media.year === 'number' && media.year > 0 ? String(media.year) : '',
    ]
        .filter((value): value is string => typeof value === 'string' && value.trim().length > 0)
        .map((value) => value.trim());

    return Array.from(new Set(parts)).join('\n');
};

export const buildMediaSearchIndex = (media: any) => normalizeSearchTerm(buildMediaSearchText(media));
