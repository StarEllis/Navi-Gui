const collapseWhitespace = (value: string) => value.replace(/\s+/g, ' ').trim();
type PinyinFunction = (value: string, options: Record<string, unknown>) => string[];

let pinyinLoader: Promise<PinyinFunction> | null = null;

const loadPinyin = () => {
    if (!pinyinLoader) {
        pinyinLoader = import('pinyin-pro').then((module) => module.pinyin as unknown as PinyinFunction);
    }
    return pinyinLoader;
};

const cjkVariantMap: Record<string, string> = {
    '沢': '泽',
    '澤': '泽',
    '鈴': '铃',
    '瀨': '濑',
    '瀬': '濑',
};

const cjkVariantPattern = new RegExp(`[${Object.keys(cjkVariantMap).join('')}]`, 'g');
const cjkCharacterPattern = /[\u3005\u3400-\u4dbf\u4e00-\u9fff\uf900-\ufaff\u3040-\u30ff]/;
const asciiAlphaNumericPattern = /[a-z0-9]/i;
const searchTokenCharacterPattern = /[0-9A-Za-z_.-]/;
const searchTokenPattern = /^[0-9A-Za-z_.-]+$/;

export type ParsedMediaSearchQuery = {
    normalized: string;
    tokens: string[];
    cjkTokens: string[];
};

export type MediaPhoneticSearchIndex = {
    pinyinIndex: string;
    compactPinyinIndex: string;
    initialsIndex: string;
};

export const normalizeCJKVariants = (value: string) => value.replace(
    cjkVariantPattern,
    (char) => cjkVariantMap[char] || char,
);

export const normalizeSearchTerm = (value: string) => collapseWhitespace(
    normalizeCJKVariants(value)
        .normalize('NFKC')
        .toLowerCase()
        .replace(/[\u0000-\u001f]+/g, ' ')
        .replace(/[_\-./\\[\](){}#+]+/g, ' '),
);

export const shouldReplaceActorFilterOnSearchChange = (
    filterType: string | undefined,
    currentValue: string,
    nextValue: string,
) => filterType === 'actor' && nextValue !== currentValue;

export const hasCJKSearchCharacter = (value: string) => cjkCharacterPattern.test(value);

// 番号里的连字符会被浏览器当成分词符，双击只能选中一半，这里把选区补成完整的一段。
export const expandSearchTokenRange = (value: string, start: number, end: number) => {
    if (start >= end || !searchTokenPattern.test(value.slice(start, end))) {
        return { start, end };
    }

    let tokenStart = start;
    let tokenEnd = end;
    while (tokenStart > 0 && searchTokenCharacterPattern.test(value[tokenStart - 1])) {
        tokenStart -= 1;
    }
    while (tokenEnd < value.length && searchTokenCharacterPattern.test(value[tokenEnd])) {
        tokenEnd += 1;
    }

    return { start: tokenStart, end: tokenEnd };
};

const uniqueInOrder = (values: string[]) => {
    const seen = new Set<string>();
    return values.filter((value) => {
        if (!value || seen.has(value)) {
            return false;
        }
        seen.add(value);
        return true;
    });
};

export const tokenizeSearchInput = (value: string) => {
    const normalized = normalizeSearchTerm(value);
    const tokens: string[] = [];
    let current = '';
    let currentKind: 'cjk' | 'latin' | '' = '';

    const pushCurrent = () => {
        if (current) {
            tokens.push(current);
            current = '';
            currentKind = '';
        }
    };

    Array.from(normalized).forEach((char) => {
        const nextKind = cjkCharacterPattern.test(char)
            ? 'cjk'
            : asciiAlphaNumericPattern.test(char)
                ? 'latin'
                : '';

        if (!nextKind) {
            pushCurrent();
            return;
        }

        if (current && currentKind !== nextKind) {
            pushCurrent();
        }

        current += char;
        currentKind = nextKind;
    });

    pushCurrent();
    return uniqueInOrder(tokens);
};

export const parseMediaSearchQuery = (value: string): ParsedMediaSearchQuery => {
    const tokens = tokenizeSearchInput(value);
    return {
        normalized: normalizeSearchTerm(value),
        tokens,
        cjkTokens: tokens.filter(hasCJKSearchCharacter),
    };
};

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

const buildPinyinTokens = async (value: string) => {
    const pinyin = await loadPinyin();
    const tokens = pinyin(normalizeCJKVariants(value), {
        toneType: 'none',
        type: 'array',
        nonZh: 'removed',
    });

    return uniqueInOrder(tokens.map(normalizeSearchField).filter(Boolean));
};

export const buildMediaPhoneticSearchIndex = async (media: any): Promise<MediaPhoneticSearchIndex> => {
    const tokens = await buildPinyinTokens(buildMediaSearchText(media));
    return {
        pinyinIndex: tokens.join(' '),
        compactPinyinIndex: tokens.join(''),
        initialsIndex: tokens.map((token) => token[0] || '').join(''),
    };
};
