import React, { useEffect, useMemo, useRef, useState } from 'react';
import { Clipboard, ImagePlus, Link2, LoaderCircle, RotateCcw, Undo2, Upload, X } from 'lucide-react';
import {
    GetPosterEditorState,
    RestoreMediaPoster,
    SaveMediaPoster,
    SelectPosterImageFile,
} from '../../wailsjs/go/main/App';
import type { AppMedia } from '../types/wails';
import type { StatusKind } from '../types/status';
import { formatError, toLocalAssetUrl } from '../utils/media';

type SourceTab = 'local' | 'stills' | 'link';
type SourceKind = 'path' | 'url' | 'data';
type CropMode = 'original' | 'ratio';
type Corner = 'top_left' | 'top_right' | 'bottom_right' | 'bottom_left';
type WatermarkGroup = 'subtitle' | 'type' | 'quality';

type PosterSource = {
    kind: SourceKind;
    value: string;
    url: string;
    name: string;
    width: number;
    height: number;
};

type WatermarkConfig = {
    subtitle: boolean;
    type_mark: string;
    quality_mark: string;
    subtitle_corner: Corner;
    type_corner: Corner;
    quality_corner: Corner;
    size: number;
};

type CropBox = {
    x: number;
    y: number;
    width: number;
    height: number;
};

type RecentPoster = {
    path: string;
    name: string;
};

interface PosterEditorModalProps {
    media: AppMedia;
    previews: string[];
    onClose: () => void;
    onSaved: (media: AppMedia) => void;
    onStatus?: (message: string, kind?: StatusKind) => void;
}

const RECENT_POSTERS_KEY = 'navi.poster-editor.recents.v1';
const MAX_SOURCE_BYTES = 20 * 1024 * 1024;
const STAGE_HEIGHT = 318;
const PILL_RATIO = 1000 / 600;
const SQUARE_RATIO = 1510 / 1370;
const CORNERS: Corner[] = ['top_left', 'top_right', 'bottom_right', 'bottom_left'];

const DEFAULT_CONFIG: WatermarkConfig = {
    subtitle: false,
    type_mark: '',
    quality_mark: '',
    subtitle_corner: 'top_left',
    type_corner: 'top_right',
    quality_corner: 'bottom_right',
    size: 5,
};

const TYPE_OPTIONS = [
    { id: 'youma', label: '有码', ratio: PILL_RATIO },
    { id: 'wuma', label: '无码', ratio: PILL_RATIO },
    { id: 'umr', label: '破解', ratio: PILL_RATIO },
    { id: 'leak', label: '流出', ratio: PILL_RATIO },
];

const QUALITY_OPTIONS = [
    { id: '4k', label: '4K', ratio: SQUARE_RATIO },
    { id: '8k', label: '8K', ratio: SQUARE_RATIO },
];

const clamp = (value: number, min: number, max: number) => Math.max(min, Math.min(max, value));
const pathName = (path: string) => path.split(/[\\/]/).pop() || path;

const imageURLForPath = (path: string) => `${toLocalAssetUrl(path)}?poster-editor=${Date.now()}`;

const loadRecentPosters = (): RecentPoster[] => {
    try {
        const value = JSON.parse(localStorage.getItem(RECENT_POSTERS_KEY) || '[]');
        if (!Array.isArray(value)) {
            return [];
        }
        return value
            .filter((item) => typeof item?.path === 'string' && item.path.trim())
            .slice(0, 5)
            .map((item) => ({ path: item.path, name: item.name || pathName(item.path) }));
    } catch (_error) {
        return [];
    }
};

const normalizeConfig = (value: any): WatermarkConfig => ({
    subtitle: value?.subtitle === true,
    type_mark: TYPE_OPTIONS.some((item) => item.id === value?.type_mark) ? value.type_mark : '',
    quality_mark: QUALITY_OPTIONS.some((item) => item.id === value?.quality_mark) ? value.quality_mark : '',
    subtitle_corner: CORNERS.includes(value?.subtitle_corner) ? value.subtitle_corner : 'top_left',
    type_corner: CORNERS.includes(value?.type_corner) ? value.type_corner : 'top_right',
    quality_corner: CORNERS.includes(value?.quality_corner) ? value.quality_corner : 'bottom_right',
    size: clamp(Number(value?.size) || 5, 1, 12),
});

const readFileAsDataURL = (file: File) => new Promise<string>((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result || ''));
    reader.onerror = () => reject(reader.error || new Error('读取图片失败'));
    reader.readAsDataURL(file);
});

const cropForSource = (source: PosterSource, mode: CropMode, zoom: number, pan: { x: number; y: number }): CropBox => {
    if (mode === 'original') {
        return { x: 0, y: 0, width: source.width, height: source.height };
    }
    let baseWidth = Math.floor(source.height / 1.5);
    let baseHeight = source.height;
    if (baseWidth > source.width) {
        baseWidth = source.width;
        baseHeight = Math.floor(source.width * 1.5);
    }
    const scale = clamp(zoom, 100, 220) / 100;
    const width = Math.max(1, Math.floor(baseWidth / scale));
    const height = Math.max(1, Math.floor(baseHeight / scale));
    const x = Math.round(((clamp(pan.x, -1, 1) + 1) / 2) * Math.max(0, source.width - width));
    const y = Math.round(((clamp(pan.y, -1, 1) + 1) / 2) * Math.max(0, source.height - height));
    return { x, y, width, height };
};

const sourceFromPath = (path: string): PosterSource => ({
    kind: 'path', value: path, url: imageURLForPath(path), name: pathName(path), width: 0, height: 0,
});

const cornerForGroup = (config: WatermarkConfig, group: WatermarkGroup): Corner => {
    if (group === 'subtitle') return config.subtitle_corner;
    if (group === 'type') return config.type_corner;
    return config.quality_corner;
};

const setGroupCorner = (config: WatermarkConfig, group: WatermarkGroup, corner: Corner): WatermarkConfig => {
    if (group === 'subtitle') return { ...config, subtitle_corner: corner };
    if (group === 'type') return { ...config, type_corner: corner };
    return { ...config, quality_corner: corner };
};

const nearestCorner = (x: number, y: number, width: number, height: number): Corner => (
    `${y < height / 2 ? 'top' : 'bottom'}_${x < width / 2 ? 'left' : 'right'}` as Corner
);

const PosterEditorModal: React.FC<PosterEditorModalProps> = ({ media, previews, onClose, onSaved, onStatus }) => {
    const [tab, setTab] = useState<SourceTab>('local');
    const [source, setSource] = useState<PosterSource | null>(null);
    const [config, setConfig] = useState<WatermarkConfig>(DEFAULT_CONFIG);
    const [assets, setAssets] = useState<Record<string, string>>({});
    const [canRestore, setCanRestore] = useState(false);
    const [cropMode, setCropMode] = useState<CropMode>('original');
    const [zoom, setZoom] = useState(100);
    const [pan, setPan] = useState({ x: 0, y: 0 });
    const [urlInput, setURLInput] = useState('');
    const [recent, setRecent] = useState<RecentPoster[]>(loadRecentPosters);
    const [loading, setLoading] = useState(true);
    const [saving, setSaving] = useState(false);
    const [draggedMark, setDraggedMark] = useState<WatermarkGroup | null>(null);
    const [markPointer, setMarkPointer] = useState<{ x: number; y: number } | null>(null);
    const stageRef = useRef<HTMLDivElement | null>(null);
    const panStartRef = useRef<{ x: number; y: number; panX: number; panY: number } | null>(null);

    const showStatus = (message: string, kind: StatusKind = 'info') => onStatus?.(message, kind);

    const selectSource = (next: PosterSource, nextTab: SourceTab = tab) => {
        setSource(next);
        setTab(nextTab);
        setZoom(100);
        setPan({ x: 0, y: 0 });
    };

    const addRecent = (path: string) => {
        const next = [{ path, name: pathName(path) }, ...recent.filter((item) => item.path !== path)].slice(0, 5);
        setRecent(next);
        localStorage.setItem(RECENT_POSTERS_KEY, JSON.stringify(next));
    };

    useEffect(() => {
        let active = true;
        GetPosterEditorState(media.id)
            .then((state: any) => {
                if (!active) return;
                setConfig(normalizeConfig(state?.config));
                setAssets(state?.watermark_assets || {});
                setCanRestore(state?.can_restore === true);
                if (typeof state?.source_path === 'string' && state.source_path.trim()) {
                    setSource(sourceFromPath(state.source_path));
                } else if (previews[0]) {
                    setSource(sourceFromPath(previews[0]));
                    setTab('stills');
                }
            })
            .catch((error: unknown) => showStatus(`加载封面设置失败：${formatError(error)}`, 'error'))
            .finally(() => {
                if (active) setLoading(false);
            });
        return () => { active = false; };
    }, [media.id]);

    useEffect(() => {
        const handleKeyDown = (event: KeyboardEvent) => {
            if (event.key === 'Escape' && !saving) {
                onClose();
            }
        };
        const handlePaste = (event: ClipboardEvent) => {
            const image = Array.from(event.clipboardData?.items || []).find((item) => item.type.startsWith('image/'));
            const file = image?.getAsFile();
            if (!file) return;
            event.preventDefault();
            if (file.size > MAX_SOURCE_BYTES) {
                showStatus('图片超过 20MB', 'error');
                return;
            }
            void readFileAsDataURL(file)
                .then((data) => selectSource({
                    kind: 'data', value: data, url: data, name: '剪贴板图片', width: 0, height: 0,
                }, 'link'))
                .catch((error) => showStatus(formatError(error), 'error'));
        };
        document.addEventListener('keydown', handleKeyDown);
        document.addEventListener('paste', handlePaste);
        return () => {
            document.removeEventListener('keydown', handleKeyDown);
            document.removeEventListener('paste', handlePaste);
        };
    }, [onClose, saving, tab, recent]);

    const crop = useMemo(() => source && source.width > 0 && source.height > 0
        ? cropForSource(source, cropMode, zoom, pan)
        : null, [cropMode, pan, source, zoom]);
    const stageWidth = source && source.width > 0 && source.height > 0 && cropMode === 'original'
        ? Math.round(STAGE_HEIGHT * source.width / source.height)
        : Math.round(STAGE_HEIGHT * 2 / 3);
    const stageImageStyle = source && crop ? {
        width: `${source.width * stageWidth / crop.width}px`,
        height: `${source.height * STAGE_HEIGHT / crop.height}px`,
        left: `${-crop.x * stageWidth / crop.width}px`,
        top: `${-crop.y * STAGE_HEIGHT / crop.height}px`,
    } : undefined;

    const activeMarks = useMemo(() => {
        const marks: Array<{ group: WatermarkGroup; name: string; label: string; ratio: number; corner: Corner }> = [];
        if (config.subtitle) {
            marks.push({ group: 'subtitle', name: 'sub', label: '字幕', ratio: PILL_RATIO, corner: config.subtitle_corner });
        }
        const type = TYPE_OPTIONS.find((item) => item.id === config.type_mark);
        if (type) marks.push({ group: 'type', name: type.id, label: type.label, ratio: type.ratio, corner: config.type_corner });
        const quality = QUALITY_OPTIONS.find((item) => item.id === config.quality_mark);
        if (quality) marks.push({ group: 'quality', name: quality.id, label: quality.label, ratio: quality.ratio, corner: config.quality_corner });
        return marks;
    }, [config]);

    const markHeight = STAGE_HEIGHT * config.size / 40;

    const handleChooseLocal = async () => {
        try {
            const path = await SelectPosterImageFile();
            if (!path) return;
            addRecent(path);
            selectSource(sourceFromPath(path), 'local');
        } catch (error) {
            showStatus(`选择图片失败：${formatError(error)}`, 'error');
        }
    };

    const handleDroppedFile = async (file: File & { path?: string }) => {
        if (!file.type.match(/^image\/(jpeg|png|webp)$/) && !/\.(jpe?g|png|webp)$/i.test(file.name)) {
            showStatus('请选择 JPG、PNG 或 WebP 图片', 'error');
            return;
        }
        if (file.size > MAX_SOURCE_BYTES) {
            showStatus('图片超过 20MB', 'error');
            return;
        }
        if (file.path) {
            addRecent(file.path);
            selectSource(sourceFromPath(file.path), 'local');
            return;
        }
        try {
            const data = await readFileAsDataURL(file);
            selectSource({ kind: 'data', value: data, url: data, name: file.name, width: 0, height: 0 }, 'local');
        } catch (error) {
            showStatus(formatError(error), 'error');
        }
    };

    const handleFetchURL = () => {
        try {
            const parsed = new URL(urlInput.trim());
            if (!['http:', 'https:'].includes(parsed.protocol)) throw new Error('链接格式无效');
            selectSource({
                kind: 'url', value: parsed.toString(), url: parsed.toString(),
                name: pathName(parsed.pathname) || '链接图片', width: 0, height: 0,
            }, 'link');
        } catch (error) {
            showStatus(`获取图片失败：${formatError(error)}`, 'error');
        }
    };

    const handleImageLoaded = (event: React.SyntheticEvent<HTMLImageElement>) => {
        const width = event.currentTarget.naturalWidth;
        const height = event.currentTarget.naturalHeight;
        if (!width || !height) return;
        setSource((current) => current ? { ...current, width, height } : current);
        setCropMode(height / width >= 1.4 ? 'original' : 'ratio');
        setZoom(100);
        setPan({ x: 0, y: 0 });
    };

    const handleStagePointerDown = (event: React.PointerEvent<HTMLDivElement>) => {
        if (!source || cropMode !== 'ratio' || draggedMark) return;
        event.currentTarget.setPointerCapture(event.pointerId);
        panStartRef.current = { x: event.clientX, y: event.clientY, panX: pan.x, panY: pan.y };
    };

    const handleStagePointerMove = (event: React.PointerEvent<HTMLDivElement>) => {
        const start = panStartRef.current;
        if (!start || !stageRef.current) return;
        const rect = stageRef.current.getBoundingClientRect();
        setPan({
            x: clamp(start.panX - (event.clientX - start.x) * 2 / Math.max(1, rect.width), -1, 1),
            y: clamp(start.panY - (event.clientY - start.y) * 2 / Math.max(1, rect.height), -1, 1),
        });
    };

    const finishStagePan = () => { panStartRef.current = null; };

    const handleMarkDown = (event: React.PointerEvent<HTMLImageElement>, group: WatermarkGroup) => {
        event.stopPropagation();
        const rect = stageRef.current?.getBoundingClientRect();
        if (!rect) return;
        event.currentTarget.setPointerCapture(event.pointerId);
        setDraggedMark(group);
        setMarkPointer({ x: event.clientX - rect.left, y: event.clientY - rect.top });
    };

    const handleMarkMove = (event: React.PointerEvent<HTMLImageElement>) => {
        if (!draggedMark) return;
        const rect = stageRef.current?.getBoundingClientRect();
        if (!rect) return;
        setMarkPointer({ x: event.clientX - rect.left, y: event.clientY - rect.top });
    };

    const handleMarkUp = (event: React.PointerEvent<HTMLImageElement>) => {
        if (!draggedMark) return;
        const rect = stageRef.current?.getBoundingClientRect();
        if (rect) {
            setConfig((current) => setGroupCorner(
                current,
                draggedMark,
                nearestCorner(event.clientX - rect.left, event.clientY - rect.top, rect.width, rect.height),
            ));
        }
        setDraggedMark(null);
        setMarkPointer(null);
    };

    const handleSave = async () => {
        if (!source || !crop) {
            showStatus('请先选择一张可用图片', 'error');
            return;
        }
        setSaving(true);
        try {
            const updated = await SaveMediaPoster({
                media_id: media.id,
                source_kind: source.kind,
                source: source.value,
                crop_mode: cropMode,
                crop_x: crop.x,
                crop_y: crop.y,
                crop_width: crop.width,
                crop_height: crop.height,
                watermarks: config,
            } as any);
            onSaved(updated as AppMedia);
            showStatus('封面图已保存', 'info');
            onClose();
        } catch (error) {
            showStatus(`保存封面失败：${formatError(error)}`, 'error');
        } finally {
            setSaving(false);
        }
    };

    const handleRestore = async () => {
        setSaving(true);
        try {
            const updated = await RestoreMediaPoster(media.id);
            onSaved(updated as AppMedia);
            showStatus('已恢复刮削原图', 'info');
            onClose();
        } catch (error) {
            showStatus(`恢复原图失败：${formatError(error)}`, 'error');
        } finally {
            setSaving(false);
        }
    };

    const renderCornerPicker = (group: WatermarkGroup) => (
        <div className="poster-corner-picker" aria-label="水印位置">
            {CORNERS.map((corner) => (
                <button
                    key={corner}
                    type="button"
                    className={`${corner} ${cornerForGroup(config, group) === corner ? 'active' : ''}`}
                    aria-label={corner}
                    onClick={() => setConfig((current) => setGroupCorner(current, group, corner))}
                />
            ))}
        </div>
    );

    const dragged = activeMarks.find((mark) => mark.group === draggedMark);

    return (
        <div className="poster-editor-overlay" role="presentation" onMouseDown={(event) => {
            if (event.target === event.currentTarget && !saving) onClose();
        }}>
            <section className="poster-editor-modal" role="dialog" aria-modal="true" aria-labelledby="poster-editor-title">
                <header className="poster-editor-header">
                    <div className="poster-editor-heading">
                        <strong id="poster-editor-title">更换封面图</strong>
                        <span>{media.code || media.title || media.id.slice(0, 8)} · {pathName(media.poster_path || 'poster.jpg')}</span>
                    </div>
                    <div className="poster-editor-header-actions">
                        {canRestore && (
                            <button type="button" className="poster-editor-restore" disabled={saving} onClick={() => void handleRestore()}>
                                <Undo2 size={13} /> 恢复原图
                            </button>
                        )}
                        <button type="button" className="poster-editor-close" aria-label="关闭" disabled={saving} onClick={onClose}>
                            <X size={15} />
                        </button>
                    </div>
                </header>

                <div className="poster-editor-body">
                    <aside className="poster-source-panel">
                        <div className="poster-source-tabs" role="tablist">
                            {([['local', '本地'], ['stills', '剧照'], ['link', '链接']] as const).map(([id, label]) => (
                                <button key={id} type="button" className={tab === id ? 'active' : ''} onClick={() => setTab(id)}>{label}</button>
                            ))}
                        </div>

                        {tab === 'local' && (
                            <div className="poster-source-content">
                                <button
                                    type="button"
                                    className="poster-drop-zone"
                                    onClick={() => void handleChooseLocal()}
                                    onDragOver={(event) => { event.preventDefault(); event.dataTransfer.dropEffect = 'copy'; }}
                                    onDrop={(event) => {
                                        event.preventDefault();
                                        const file = event.dataTransfer.files[0] as (File & { path?: string }) | undefined;
                                        if (file) void handleDroppedFile(file);
                                    }}
                                >
                                    <Upload size={19} />
                                    <span>拖入图片，或点击选择</span>
                                    <small>JPG · PNG · WEBP · ≤ 20MB</small>
                                </button>
                                <div className="poster-source-label">最近使用</div>
                                <div className="poster-recent-list">
                                    {recent.length === 0 && <div className="poster-source-empty">还没有最近使用的图片</div>}
                                    {recent.map((item) => (
                                        <button key={item.path} type="button" onClick={() => selectSource(sourceFromPath(item.path), 'local')}>
                                            <span className="poster-recent-thumb"><ImagePlus size={13} /></span>
                                            <span><strong>{item.name}</strong><small title={item.path}>{item.path}</small></span>
                                        </button>
                                    ))}
                                </div>
                            </div>
                        )}

                        {tab === 'stills' && (
                            <div className="poster-source-content">
                                <div className="poster-source-label">剧照 {previews.length} 张</div>
                                {previews.length === 0 ? (
                                    <div className="poster-source-empty">该影片还没有已抽帧剧照</div>
                                ) : (
                                    <div className="poster-stills-grid">
                                        {previews.map((path, index) => (
                                            <button
                                                key={path}
                                                type="button"
                                                className={source?.kind === 'path' && source.value === path ? 'active' : ''}
                                                title={`剧照 ${index + 1}`}
                                                onClick={() => selectSource(sourceFromPath(path), 'stills')}
                                            >
                                                <img src={toLocalAssetUrl(path)} alt={`剧照 ${index + 1}`} />
                                                <span>#{String(index + 1).padStart(2, '0')}</span>
                                            </button>
                                        ))}
                                    </div>
                                )}
                            </div>
                        )}

                        {tab === 'link' && (
                            <div className="poster-source-content">
                                <div className="poster-source-label">图片链接</div>
                                <div className="poster-link-row">
                                    <div><Link2 size={13} /><input value={urlInput} onChange={(event) => setURLInput(event.target.value)} placeholder="https://…/poster.jpg" onKeyDown={(event) => {
                                        if (event.key === 'Enter') handleFetchURL();
                                    }} /></div>
                                    <button type="button" onClick={handleFetchURL}>获取</button>
                                </div>
                                <div className="poster-paste-hint"><Clipboard size={14} />剪贴板里有图片，按 Ctrl/⌘ V 直接粘贴</div>
                            </div>
                        )}
                    </aside>

                    <main className="poster-crop-panel">
                        <div className="poster-crop-meta">
                            <span>{crop ? (cropMode === 'original'
                                ? `原图直接用 · 输出 ${crop.width} × ${crop.height}`
                                : `裁剪 2 : 3 · 输出 ${crop.width} × ${crop.height}`) : '等待选择图片'}</span>
                            <span>{source ? `${source.name} · ${source.width || '—'} × ${source.height || '—'}` : '未选择'}</span>
                        </div>

                        <div
                            ref={stageRef}
                            className={`poster-crop-stage ${cropMode === 'ratio' ? 'can-pan' : ''}`}
                            style={{ width: `${stageWidth}px`, height: `${STAGE_HEIGHT}px` }}
                            onPointerDown={handleStagePointerDown}
                            onPointerMove={handleStagePointerMove}
                            onPointerUp={finishStagePan}
                            onPointerCancel={finishStagePan}
                        >
                            {source ? (
                                <img
                                    key={source.url}
                                    src={source.url}
                                    className="poster-crop-image"
                                    style={stageImageStyle}
                                    alt="封面裁剪预览"
                                    draggable={false}
                                    onLoad={handleImageLoaded}
                                    onError={() => showStatus('图片加载失败，请检查文件或链接', 'error')}
                                />
                            ) : (
                                <div className="poster-crop-placeholder"><ImagePlus size={24} /><span>选择图片后在这里裁剪</span></div>
                            )}
                            <div className="poster-thirds-grid" aria-hidden="true" />

                            {CORNERS.map((corner) => {
                                const marks = activeMarks.filter((mark) => mark.corner === corner && mark.group !== draggedMark);
                                if (marks.length === 0) return null;
                                return (
                                    <div key={corner} className={`poster-mark-stack ${corner}`}>
                                        {marks.map((mark) => (
                                            <img
                                                key={mark.group}
                                                src={assets[mark.name]}
                                                alt={mark.label}
                                                draggable={false}
                                                style={{ width: `${markHeight * mark.ratio}px`, height: `${markHeight}px` }}
                                                onPointerDown={(event) => handleMarkDown(event, mark.group)}
                                                onPointerMove={handleMarkMove}
                                                onPointerUp={handleMarkUp}
                                                onPointerCancel={handleMarkUp}
                                            />
                                        ))}
                                    </div>
                                );
                            })}

                            {draggedMark && markPointer && (
                                <>
                                    {CORNERS.map((corner) => <span key={corner} className={`poster-mark-drop ${corner} ${corner === nearestCorner(markPointer.x, markPointer.y, stageWidth, STAGE_HEIGHT) ? 'active' : ''}`} />)}
                                    {dragged && <img className="poster-mark-ghost" src={assets[dragged.name]} alt="" style={{
                                        width: `${markHeight * dragged.ratio}px`, height: `${markHeight}px`,
                                        left: `${markPointer.x - markHeight * dragged.ratio / 2}px`, top: `${markPointer.y - markHeight / 2}px`,
                                    }} />}
                                </>
                            )}
                            <span className="poster-crop-hint">{draggedMark ? '松手放到高亮的角' : cropMode === 'ratio' ? '拖动图片调整构图，拖水印换角' : '完整保留原图，拖水印换角'}</span>
                        </div>

                        <div className="poster-crop-modes">
                            <button type="button" className={cropMode === 'original' ? 'active' : ''} disabled={!source || (source.height > 0 && source.height / source.width < 1.4)} onClick={() => { setCropMode('original'); setZoom(100); setPan({ x: 0, y: 0 }); }}>原比例（不裁剪）</button>
                            <button type="button" className={cropMode === 'ratio' ? 'active' : ''} disabled={!source} onClick={() => { setCropMode('ratio'); setZoom(100); setPan({ x: 0, y: 0 }); }}>裁剪 2 : 3</button>
                        </div>
                        <div className="poster-zoom-control">
                            <span>缩放</span>
                            <input type="range" min="100" max="220" value={zoom} disabled={cropMode === 'original'} onChange={(event) => setZoom(Number(event.target.value))} />
                            <output>{zoom}%</output>
                            <button type="button" disabled={cropMode === 'original'} onClick={() => { setZoom(100); setPan({ x: 0, y: 0 }); }}><RotateCcw size={11} />复位</button>
                        </div>
                    </main>

                    <aside className="poster-watermark-panel">
                        <div className="poster-watermark-title"><strong>水印</strong><button type="button" onClick={() => setConfig((current) => ({ ...current, subtitle: false, type_mark: '', quality_mark: '' }))}>全部清除</button></div>

                        <div className="poster-watermark-group">
                            <div className="poster-watermark-group-title"><span>字幕</span>{renderCornerPicker('subtitle')}</div>
                            <button type="button" className={`poster-watermark-option wide ${config.subtitle ? 'active' : ''}`} onClick={() => setConfig((current) => ({ ...current, subtitle: !current.subtitle }))}>
                                <img src={assets.sub} alt="字幕" /><span>中文字幕</span><i />
                            </button>
                        </div>

                        <div className="poster-watermark-group">
                            <div className="poster-watermark-group-title"><span>类型 <small>单选</small></span>{renderCornerPicker('type')}</div>
                            <div className="poster-watermark-options">
                                {TYPE_OPTIONS.map((item) => (
                                    <button key={item.id} type="button" className={`poster-watermark-option ${config.type_mark === item.id ? 'active' : ''}`} onClick={() => setConfig((current) => ({ ...current, type_mark: current.type_mark === item.id ? '' : item.id }))}>
                                        <img src={assets[item.id]} alt={item.label} /><span>{item.label}</span>
                                    </button>
                                ))}
                            </div>
                        </div>

                        <div className="poster-watermark-group">
                            <div className="poster-watermark-group-title"><span>画质 <small>单选</small></span>{renderCornerPicker('quality')}</div>
                            <div className="poster-watermark-options">
                                {QUALITY_OPTIONS.map((item) => (
                                    <button key={item.id} type="button" className={`poster-watermark-option ${config.quality_mark === item.id ? 'active' : ''}`} onClick={() => setConfig((current) => ({ ...current, quality_mark: current.quality_mark === item.id ? '' : item.id }))}>
                                        <img src={assets[item.id]} alt={item.label} /><span>{item.label}</span>
                                    </button>
                                ))}
                            </div>
                        </div>

                        <div className="poster-watermark-divider" />
                        <div className="poster-watermark-size">
                            <div><span>水印大小</span><input type="range" min="1" max="12" step="1" value={config.size} onChange={(event) => setConfig((current) => ({ ...current, size: Number(event.target.value) }))} /><output>{config.size}</output></div>
                            <p>水印高度 = 大小 ÷ 40 × 封面高度，当前输出约 {crop ? Math.floor(crop.height * config.size / 40) : '—'} px。贴齐边角，留白来自 PNG 透明边。</p>
                        </div>
                        <p className="poster-watermark-help">默认字幕左上、类型右上、画质右下；也可拖到任意一角。</p>
                    </aside>
                </div>

                <footer className="poster-editor-footer">
                    <button type="button" className="secondary" disabled={saving} onClick={onClose}>取消</button>
                    <button type="button" className="primary" disabled={saving || loading || !source || !crop} onClick={() => void handleSave()}>
                        {saving && <LoaderCircle size={13} className="spin" />}保存封面
                    </button>
                </footer>
            </section>
        </div>
    );
};

export default PosterEditorModal;
