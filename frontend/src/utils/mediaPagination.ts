export const MEDIA_PAGE_SIZE = 120;
export const MAX_MEDIA_PAGE_SIZE = 200;
export const MAX_RETAINED_MEDIA_PAGES = 7;

export type MediaPageQuery = {
    libraryId: string;
    page: number;
    pageSize: number;
    searchTerm: string;
    mediaType: string;
    sortBy: string;
    sortOrder: 'asc' | 'desc';
    favorite: boolean | null;
    watched: boolean | null;
    filterType: string;
    filterValue: string;
    userFilter: string;
};

export type MediaPageResult = {
    items: any[];
    total: number;
    page: number;
    pageSize: number;
};

type PageLoader = (query: MediaPageQuery) => Promise<MediaPageResult>;

const inFlightRequests = new Map<string, Promise<MediaPageResult>>();

export const normalizeMediaPage = (page: number) => Math.max(1, Math.floor(Number.isFinite(page) ? page : 1));

export const normalizeMediaPageSize = (pageSize: number) => Math.min(
    MAX_MEDIA_PAGE_SIZE,
    Math.max(1, Math.floor(Number.isFinite(pageSize) ? pageSize : MEDIA_PAGE_SIZE)),
);

export const getMediaPageRequestKey = (query: MediaPageQuery) => JSON.stringify([
    query.libraryId,
    normalizeMediaPage(query.page),
    normalizeMediaPageSize(query.pageSize),
    query.searchTerm.trim(),
    query.mediaType,
    query.sortBy,
    query.sortOrder,
    query.favorite,
    query.watched,
    query.filterType,
    query.filterValue,
    query.userFilter,
]);

export const requestMediaPage = (query: MediaPageQuery, loader: PageLoader, requestScope = ''): Promise<MediaPageResult> => {
    const normalizedQuery = {
        ...query,
        page: normalizeMediaPage(query.page),
        pageSize: normalizeMediaPageSize(query.pageSize),
    };
    const key = JSON.stringify([requestScope, getMediaPageRequestKey(normalizedQuery)]);
    const existing = inFlightRequests.get(key);
    if (existing) {
        return existing;
    }

    const request = loader(normalizedQuery).finally(() => {
        if (inFlightRequests.get(key) === request) {
            inFlightRequests.delete(key);
        }
    });
    inFlightRequests.set(key, request);
    return request;
};

export type LoadingPageGenerations = Map<number, number>;

export const isPageLoadingForGeneration = (loading: LoadingPageGenerations, page: number, generation: number) => (
    loading.get(page) === generation
);

export const markPageLoading = (loading: LoadingPageGenerations, page: number, generation: number) => {
    if (loading.get(page) === generation) {
        return loading;
    }
    const next = new Map(loading);
    next.set(page, generation);
    return next;
};

export const finishPageLoading = (loading: LoadingPageGenerations, page: number, generation: number) => {
    if (loading.get(page) !== generation) {
        return loading;
    }
    const next = new Map(loading);
    next.delete(page);
    return next;
};

export const getPagesForVisibleRange = (
    startIndex: number,
    endIndex: number,
    total: number,
    pageSize = MEDIA_PAGE_SIZE,
) => {
    if (total <= 0 || endIndex <= startIndex) {
        return [];
    }

    const normalizedSize = normalizeMediaPageSize(pageSize);
    const totalPages = Math.max(1, Math.ceil(total / normalizedSize));
    const firstPage = Math.max(1, Math.floor(Math.max(0, startIndex) / normalizedSize) + 1);
    const lastItemIndex = Math.min(total, Math.max(startIndex + 1, endIndex)) - 1;
    const lastPage = Math.min(totalPages, Math.floor(lastItemIndex / normalizedSize) + 1);
    const pages: number[] = [];
    for (let page = Math.max(1, firstPage - 1); page <= Math.min(totalPages, lastPage + 1); page += 1) {
        pages.push(page);
    }
    return pages;
};

export const putMediaPage = (
    current: Map<number, any[]>,
    page: number,
    items: any[],
    protectedPages: number[],
    maxPages = MAX_RETAINED_MEDIA_PAGES,
) => {
    const next = new Map(current);
    next.delete(page);
    next.set(page, items);
    const protectedSet = new Set([...protectedPages, page]);

    for (const cachedPage of next.keys()) {
        if (next.size <= maxPages) {
            break;
        }
        if (!protectedSet.has(cachedPage)) {
            next.delete(cachedPage);
        }
    }
    return next;
};

export const getMediaAtIndex = (pages: Map<number, any[]>, index: number, pageSize = MEDIA_PAGE_SIZE) => {
    const normalizedSize = normalizeMediaPageSize(pageSize);
    const page = Math.floor(index / normalizedSize) + 1;
    return pages.get(page)?.[index % normalizedSize] || null;
};

let nextRequestGateID = 0;

export const createLatestRequestGate = () => {
    nextRequestGateID += 1;
    const gateID = nextRequestGateID;
    let generation = 0;
    let active = true;
    return {
        next: () => {
            generation += 1;
            return generation;
        },
        current: () => generation,
        accepts: (candidate: number) => active && candidate === generation,
        requestScope: (candidate: number) => `${gateID}:${candidate}`,
        dispose: () => {
            active = false;
            generation += 1;
        },
    };
};

export const getVirtualGridWindow = (
    total: number,
    columns: number,
    scrollTop: number,
    viewportHeight: number,
    rowHeight: number,
    overscanRows: number,
) => {
    const safeColumns = Math.max(1, Math.floor(columns));
    const safeTotal = Math.max(0, Math.floor(total));
    const totalRows = Math.ceil(safeTotal / safeColumns);
    const startRow = Math.min(
        Math.max(0, totalRows - 1),
        Math.max(0, Math.floor(Math.max(0, scrollTop) / rowHeight) - overscanRows),
    );
    const endRow = Math.min(
        totalRows,
        Math.ceil((Math.max(0, scrollTop) + Math.max(rowHeight, viewportHeight)) / rowHeight) + overscanRows,
    );
    return {
        totalRows,
        startRow,
        endRow,
        startIndex: startRow * safeColumns,
        endIndex: Math.min(safeTotal, endRow * safeColumns),
    };
};
