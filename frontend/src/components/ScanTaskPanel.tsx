import React, { useEffect, useRef, useState, useSyncExternalStore } from 'react';
import { Check, ChevronDown, ChevronUp, LoaderCircle, TriangleAlert } from 'lucide-react';
import { markComponentRender } from '../utils/performanceDiagnostics';
import { scanProgressStore } from '../utils/scanProgressStore';

const TERMINAL_HOLD_MS = 3000;
const TERMINAL_FADE_MS = 280;

// 完整路径太长，卡片只有 300px，留最后两段就够认出扫到哪了。
const tailPathSegments = (value: string, count = 2) => {
    const segments = value.split(/[\\/]/).filter(Boolean);
    return segments.length > count ? segments.slice(-count).join('\\') : value;
};

const formatElapsed = (ms: number) => {
    const seconds = Math.max(0, Math.round(ms / 1000));
    return seconds >= 60 ? `${Math.floor(seconds / 60)}m ${seconds % 60}s` : `${seconds}s`;
};

const ScanTaskPanel: React.FC<{ stacked: boolean; libraryId: string }> = ({ stacked, libraryId }) => {
    markComponentRender('ScanTaskPanel');
    const progress = useSyncExternalStore(
        scanProgressStore.subscribe,
        () => scanProgressStore.getSnapshot(libraryId),
        () => scanProgressStore.getSnapshot(libraryId),
    );
    const terminal = useSyncExternalStore(
        scanProgressStore.subscribe,
        () => scanProgressStore.getLibraryState(libraryId).recentTerminal,
        () => scanProgressStore.getLibraryState(libraryId).recentTerminal,
    );
    const [collapsed, setCollapsed] = useState(false);
    const [leaving, setLeaving] = useState(false);
    const startedAtRef = useRef<{ taskId: string; at: number } | null>(null);
    const finishedAtRef = useRef<{ taskId: string; elapsed: number } | null>(null);

    if (progress && startedAtRef.current?.taskId !== progress.taskId) {
        startedAtRef.current = { taskId: progress.taskId, at: Date.now() };
    }
    if (terminal && finishedAtRef.current?.taskId !== terminal.taskId) {
        const started = startedAtRef.current;
        finishedAtRef.current = {
            taskId: terminal.taskId,
            elapsed: started && started.taskId === terminal.taskId ? Date.now() - started.at : 0,
        };
    }

    // 终态卡片就地停 3 秒再淡出，不跳成另一个东西。
    useEffect(() => {
        if (!terminal) {
            setLeaving(false);
            return;
        }
        setLeaving(false);
        const holdTimer = window.setTimeout(() => setLeaving(true), TERMINAL_HOLD_MS);
        const clearTimer = window.setTimeout(
            () => scanProgressStore.clearHistory(terminal.libraryId),
            TERMINAL_HOLD_MS + TERMINAL_FADE_MS,
        );
        return () => {
            window.clearTimeout(holdTimer);
            window.clearTimeout(clearTimer);
        };
    }, [terminal]);

    useEffect(() => {
        if (progress) {
            setCollapsed(false);
        }
    }, [progress?.taskId]);

    if (progress) {
        const hasTotal = progress.total > 0;
        const total = Math.max(progress.current, progress.total);
        const countText = hasTotal ? `${progress.current}/${total}` : `${progress.current}`;
        const percent = hasTotal ? Math.min(100, (progress.current / total) * 100) : 0;
        const fileLine = progress.message.trim();

        return (
            <div
                className={`scan-progress-panel ${stacked ? 'stacked' : ''} ${collapsed ? 'collapsed' : ''}`.trim()}
                role="status"
                aria-live="polite"
            >
                <div className="scan-progress-head">
                    <LoaderCircle size={14} className="scan-progress-spinner" />
                    <span className="scan-progress-title">
                        正在扫描 {progress.libraryName}
                    </span>
                    <span className="scan-progress-count">{countText}</span>
                    <button
                        type="button"
                        className="scan-progress-toggle"
                        onClick={() => setCollapsed((prev) => !prev)}
                        aria-label={collapsed ? '展开扫描进度' : '收起扫描进度'}
                    >
                        {collapsed ? <ChevronUp size={14} /> : <ChevronDown size={14} />}
                    </button>
                </div>

                {!collapsed && (
                    <>
                        <div className={`scan-progress-track ${hasTotal ? '' : 'indeterminate'}`.trim()}>
                            <span style={hasTotal ? { width: `${percent}%` } : undefined} />
                        </div>
                        {fileLine && (
                            <div className="scan-progress-file" title={fileLine}>
                                {tailPathSegments(fileLine)}
                            </div>
                        )}
                    </>
                )}
            </div>
        );
    }

    if (!terminal) {
        return null;
    }

    const succeeded = terminal.phase === 'completed';
    const failed = terminal.phase === 'failed' || terminal.phase === 'incomplete';
    const terminalCount = terminal.total > 0 ? terminal.total : terminal.current;
    const terminalTitle = succeeded
        ? '扫描完成'
        : terminal.phase === 'canceled'
            ? '扫描已取消'
            : terminal.message.trim() || '扫描失败';

    return (
        <div
            className={`scan-progress-panel terminal ${stacked ? 'stacked' : ''} ${failed ? 'failed' : ''} ${leaving ? 'leaving' : ''}`.trim()}
            role="status"
            aria-live="polite"
        >
            <div className="scan-progress-head">
                {succeeded
                    ? <Check size={14} className="scan-progress-icon-ok" />
                    : <TriangleAlert size={14} className={failed ? 'scan-progress-icon-bad' : 'scan-progress-icon-muted'} />}
                <span className="scan-progress-title" title={terminalTitle}>{terminalTitle}</span>
                {succeeded && (
                    <span className="scan-progress-count">
                        {terminalCount} 部 · {formatElapsed(finishedAtRef.current?.elapsed || 0)}
                    </span>
                )}
            </div>
        </div>
    );
};

export default React.memo(ScanTaskPanel);
