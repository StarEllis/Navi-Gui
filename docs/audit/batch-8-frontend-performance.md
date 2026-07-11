# Batch 8 frontend performance baseline and results

Measurement date: 2026-07-11. The baseline was captured before the Batch 8 production changes with `frontend/benchmark.html`, Chromium Performance APIs, React Profiler, component render counters, and controlled mock Wails data. Timing values are observations, not CI thresholds.

## Baseline

| Scenario | Observed result |
|---|---|
| 100 items, first view | 1 Wails request returning 100 items; 30 card/image nodes; 327 DOM nodes; 6 commits; 5.5 ms accumulated Profiler duration; 15.02 MB JS heap |
| 1,000 items, first view | 1 Wails request returning 1,000 items; 30 card/image nodes; 327 DOM nodes; 8 commits; 7.4 ms accumulated Profiler duration; 29.66 MB JS heap |
| 10,000 items, first view | 1 Wails request returning 10,000 items (`size=0`); 30 card/image nodes; 327 DOM nodes; 7 commits; 44.6 ms accumulated Profiler duration; 1 long task; 80 MB JS heap |
| 10,000 item scroll | 42 cards rendered at the sampled overscan position; scrollTop 4,010; grid remained windowed, but all 10,000 media objects and search indexes stayed resident |
| Rapid search input (10 changes) | 1 initial full-library request and no server search request; 49 MediaGrid renders; final local result was current; stale server overwrite was not exercised because search ran entirely client-side |
| Detail open and return | scrollTop 4,010 was retained; no additional Wails request; grid remained mounted; 13 total grid commits in the sampled load/scroll/open/return run |
| 100 scan progress updates | 109 MediaGrid renders/Profiler commits including startup/layout commits; grid cards were memoized but the grid component still rendered for every parent progress update |
| Images | 30 initial image elements at the sampled viewport; native `loading=lazy`; fixed card CSS limited visible requests, but the error fallback pointed to a remote URL and could retry through `onError` |

The in-app browser exposed `performance.memory`; heap values vary by browser process history and are used only as a trend. It did not expose reliable frame-by-frame dropped-frame attribution, Wails transport cancellation, or native WebView process memory, so those are not claimed here.

## After Batch 8

The same browser, viewport, controlled data generator, and benchmark entry were used after the implementation.

| Scenario | Baseline | After Batch 8 |
|---|---|---|
| 100 items, settled first view | 6 commits; 5.5 ms accumulated Profiler work; 15.02 MB heap | 4 commits; 7.5 ms accumulated Profiler work; 12.46 MB heap; 1 request returning 100 items |
| 1,000 items, settled first view | 8 commits; 7.4 ms accumulated Profiler work; 29.66 MB heap | 5 commits; 7.0 ms accumulated Profiler work; 12.95 MB heap; 2 requests returning at most 120 items each |
| 10,000 items, settled first view | 7 commits; 44.6 ms accumulated Profiler work; 80 MB heap; 1 long task | 6 commits; 8.0 ms accumulated Profiler work; 14.33 MB heap; 0 long tasks; 2 requests returning at most 120 items each |
| 10,000 item DOM | 327 total DOM nodes, 30 cards initially, 42 at sampled scroll | 357 total DOM nodes, 30 cards initially, 42 at sampled scroll; deterministic model caps this viewport at 42 cards |
| Scroll sample | 42 rendered cards; all 10,000 media/search-index objects resident | 42 rendered cards; only visible/adjacent server pages resident; maximum 7 retained pages |
| Image load | 30 visible image elements; native lazy loading; remote fallback could retry | 30 peak visible image requests; async decode; fixed 178:255 aspect ratio; failed image hides once over a local stable placeholder |
| Rapid search input (10 changes) | 0 server search requests because all 10,000 rows were filtered locally; 49 MediaGrid renders | 1 request for the final term (`Media 10`), 0 duplicate requests, current final result, 9 total grid renders including startup/layout |
| Stale response protection | Not exercised by local search | Generation gate accepts only the current request; late, switched-library, retried-old-generation, and unmounted responses are rejected by deterministic tests |
| Detail open/return after scroll | scrollTop 4,010 retained; no second request; sampled 42 cards | scrollTop 4,010 retained; no full-library request; 42 cards; paged data stayed mounted under the detail overlay |
| 100 progress events | 109 MediaGrid renders/commits including startup | 0 additional MediaGrid renders (6 startup/layout renders remained); ScanTaskPanel received 100 updates plus 1 initial render |
| One favorite mutation | Not separately instrumented | 1 additional MediaCard render; other visible cards did not render |

DOM size did not materially fall because the pre-change grid already had a basic virtual window. The material improvement is removal of the full-library response, full search-index copy, and unbounded list cache. The extra 30 post-change DOM nodes are the stable local image fallbacks underneath the visible images.

## Implementation invariants

- Server page size defaults to 120 and is capped at 200. A page beyond the last page is clamped after the filtered count.
- SQL applies library, search, media type, actor, tag/genre, Series, favorite, watched/unwatched, sort, and user predicates before `COUNT`, `OFFSET`, and `LIMIT`.
- The frontend renders a virtual total-height list, fetches visible and adjacent pages, deduplicates identical in-flight calls, and retains at most 7 pages.
- Search is debounced for 250 ms before the grid receives a new query. Query changes reset the virtual position through the route-keyed scroll state.
- Route scroll state is keyed by library/view/query/sort/filter and capped at 32 entries. The detail overlay keeps the list mounted, so return restoration has no top-then-jump frame.
- Scan progress uses a dedicated external store. Progress does not mutate list state; completion refreshes only the current library page set.
- Canceled or stale requests are ignored rather than displayed as errors. Ordinary failures retain the previous usable results for the same library and expose an inline retry action.

## Measurement limits

- `commitDuration` is accumulated React Profiler work while the first view settled, not a strict wall-clock first-content-paint threshold. The old build did not record a separate first-card timestamp, so no such number is invented.
- Chromium exposed JS heap trend and Long Task entries, but not reliable native WebView process memory, Wails transport cancellation, decoded-image memory, or frame-by-frame dropped-frame attribution.
- The sampled scroll had no visually observed stall and no Long Task entry after the change. Exact dropped-frame counts were not available.
- Image peak was measured as 30 visible image requests/elements. Exact simultaneous socket/decode concurrency was not exposed by this Wails mock benchmark.
