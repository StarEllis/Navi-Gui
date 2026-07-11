import React, { Profiler, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { createRoot } from 'react-dom/client';
import MediaGrid, { type MediaGridMutation } from '../components/MediaGrid';
import ScanTaskPanel from '../components/ScanTaskPanel';
import { scanProgressStore } from '../utils/scanProgressStore';
import '../style.css';
import '../App.css';

type RequestRecord = {
    libraryId: string;
    page: number;
    size: number;
    sortBy: string;
    sortOrder: string;
    keyword: string;
    filterType: string;
    filterValue: string;
};

type BenchmarkMetrics = {
    count: number;
    commits: number;
    commitDuration: number;
    domNodes: number;
    cardNodes: number;
    imageNodes: number;
    imageRequests: number;
    peakImageNodes: number;
    requests: RequestRecord[];
    duplicateRequests: number;
    longTasks: number;
    heapMB: number | null;
    scrollTop: number;
    renders: Record<string, number>;
};

const params = new URLSearchParams(window.location.search);
const requestedCount = Number(params.get('count') || 10000);
const DATA_COUNT = Number.isFinite(requestedCount) ? Math.max(0, Math.min(50000, requestedCount)) : 10000;
const requests: RequestRecord[] = [];

const media = Array.from({ length: DATA_COUNT }, (_, index) => ({
    id: `media-${index + 1}`,
    library_id: 'library-a',
    title: `Media ${String(index + 1).padStart(5, '0')}`,
    orig_title: `Original ${index + 1}`,
    file_path: `C:\\benchmark\\media-${index + 1}.mp4`,
    poster_path: `C:\\benchmark\\poster-${index + 1}.jpg`,
    actor: index % 2 === 0 ? 'Actor A' : 'Actor B',
    genres: index % 3 === 0 ? 'Drama' : 'Action',
    media_type: index % 5 === 0 ? 'episode' : 'movie',
    year: 2000 + (index % 25),
    is_favorite: false,
    is_watched: index % 4 === 0,
}));

window.__ALEX_PERF__ = { renders: {} };

const getFilteredItems = (request: RequestRecord) => {
    const keyword = request.keyword.trim().toLocaleLowerCase();
    let result = media.filter((item) => item.library_id === request.libraryId);
    if (keyword) {
        result = result.filter((item) => `${item.title} ${item.orig_title} ${item.actor} ${item.genres}`.toLocaleLowerCase().includes(keyword));
    }
    if (request.filterType === 'favorite') {
        result = result.filter((item) => item.is_favorite);
    } else if (request.filterType === 'watched') {
        result = result.filter((item) => item.is_watched);
    } else if (request.filterType === 'genre') {
        result = result.filter((item) => item.genres === request.filterValue);
    } else if (request.filterType === 'actor') {
        result = result.filter((item) => item.actor === request.filterValue);
    }
    return result;
};

(window as any).go = {
    main: {
        App: {
            GetMediaList: async (...args: any[]) => {
                const request: RequestRecord = {
                    libraryId: String(args[0] || ''),
                    page: Number(args[1] || 1),
                    size: Number(args[2] || 0),
                    sortBy: String(args[3] || ''),
                    sortOrder: String(args[4] || ''),
                    keyword: String(args[5] || ''),
                    filterType: String(args[6] || ''),
                    filterValue: String(args[7] || ''),
                };
                requests.push(request);
                const filtered = getFilteredItems(request);
                const start = request.size > 0 ? (request.page - 1) * request.size : 0;
                const items = request.size > 0 ? filtered.slice(start, start + request.size) : filtered;
                return { items, total: filtered.length };
            },
            GetMediaDetailBundle: async () => ({}),
            PlayFile: async () => undefined,
        },
    },
};

const longTaskState = { count: 0 };
const imageState = { peakNodes: 0 };
const imageObserver = new MutationObserver(() => {
    imageState.peakNodes = Math.max(imageState.peakNodes, document.querySelectorAll('.media-poster-image').length);
});
imageObserver.observe(document.documentElement, { childList: true, subtree: true });
if ('PerformanceObserver' in window) {
    try {
        const observer = new PerformanceObserver((list) => {
            longTaskState.count += list.getEntries().length;
        });
        observer.observe({ entryTypes: ['longtask'] });
    } catch {
        // Long Task API is not available in every WebView/browser.
    }
}

const Benchmark = () => {
    const [keyword, setKeyword] = useState('');
    const [debouncedKeyword, setDebouncedKeyword] = useState('');
    const [libraryId, setLibraryId] = useState('library-a');
    const [mutation, setMutation] = useState<MediaGridMutation | null>(null);
    const [detailOpen, setDetailOpen] = useState(false);
    const [selectedMedia, setSelectedMedia] = useState<any>(null);
    const [metrics, setMetrics] = useState<BenchmarkMetrics | null>(null);
    const commitsRef = useRef(0);
    const durationRef = useRef(0);
    const scrollTopRef = useRef(0);

    const handleSelectMedia = useCallback((item: any) => {
        setSelectedMedia(item);
        setDetailOpen(true);
    }, []);
    const handleScroll = useCallback((scrollTop: number) => {
        scrollTopRef.current = scrollTop;
    }, []);
    const captureMetrics = useCallback(() => {
        const requestKeys = requests.map((request) => JSON.stringify(request));
        const heap = (performance as any).memory?.usedJSHeapSize;
        setMetrics({
            count: DATA_COUNT,
            commits: commitsRef.current,
            commitDuration: Number(durationRef.current.toFixed(2)),
            domNodes: document.querySelectorAll('*').length,
            cardNodes: document.querySelectorAll('.media-card').length,
            imageNodes: document.querySelectorAll('.media-poster-image').length,
            imageRequests: performance.getEntriesByType('resource').filter((entry) => entry.name.includes('/local/')).length,
            peakImageNodes: imageState.peakNodes,
            requests: [...requests],
            duplicateRequests: requestKeys.length - new Set(requestKeys).size,
            longTasks: longTaskState.count,
            heapMB: typeof heap === 'number' ? Number((heap / 1024 / 1024).toFixed(2)) : null,
            scrollTop: scrollTopRef.current,
            renders: { ...window.__ALEX_PERF__?.renders },
        });
    }, []);

    useEffect(() => {
        const timer = window.setTimeout(captureMetrics, 500);
        return () => window.clearTimeout(timer);
    }, [captureMetrics, keyword, libraryId, mutation, detailOpen]);

    useEffect(() => {
        const timer = window.setTimeout(() => setDebouncedKeyword(keyword.trim()), keyword.trim() ? 250 : 0);
        return () => window.clearTimeout(timer);
    }, [keyword]);

    const runScanBurst = () => {
        let index = 0;
        const timer = window.setInterval(() => {
            index += 1;
            scanProgressStore.set({
                taskId: 'benchmark-task',
                libraryId,
                libraryName: 'Benchmark',
                mode: 'scan',
                phase: 'progress',
                current: index,
                total: 100,
                message: `item ${index}`,
            });
            if (index === 100) {
                window.clearInterval(timer);
            }
        }, 5);
    };

    const controls = useMemo(() => (
        <div className="benchmark-controls">
            <label>Search <input aria-label="Benchmark search" value={keyword} onChange={(event) => setKeyword(event.target.value)} /></label>
            <button type="button" onClick={() => setLibraryId((value) => value === 'library-a' ? 'library-b' : 'library-a')}>Switch library</button>
            <button type="button" onClick={() => setMutation({ type: 'merge', media: { ...media[0], is_favorite: true } })}>Toggle favorite</button>
            <button type="button" onClick={runScanBurst}>Scan x100</button>
            <button type="button" onClick={() => setDetailOpen(false)}>Close detail</button>
            <button type="button" onClick={captureMetrics}>Capture</button>
        </div>
    ), [captureMetrics, keyword, libraryId]);

    return (
        <div className="benchmark-shell">
            {controls}
            <ScanTaskPanel hidden={false} libraryId={libraryId} />
            <Profiler id="MediaGrid" onRender={(_id, _phase, actualDuration) => {
                commitsRef.current += 1;
                durationRef.current += actualDuration;
            }}>
                <MediaGrid
                    libraryId={libraryId}
                    keyword={keyword.trim() ? debouncedKeyword : ''}
                    sortField="created_at"
                    sortOrder="desc"
                    onSelectMedia={handleSelectMedia}
                    onScrollPositionChange={handleScroll}
                    mutation={mutation}
                />
            </Profiler>
            {detailOpen && (
                <div className="benchmark-detail" role="dialog" aria-label="Benchmark detail">
                    <span>{selectedMedia?.title}</span>
                    <button type="button" onClick={() => setDetailOpen(false)}>Back to grid</button>
                </div>
            )}
            <pre id="benchmark-metrics">{JSON.stringify(metrics, null, 2)}</pre>
        </div>
    );
};

createRoot(document.getElementById('root')!).render(<Benchmark />);
