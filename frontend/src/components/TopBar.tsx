import React, { useEffect, useRef, useState } from 'react';
import {
    ArrowDownNarrowWide,
    ArrowLeft,
    ArrowUpNarrowWide,
    Folder,
    LayoutGrid,
    List,
    RefreshCw,
    Search,
    Shuffle,
    X,
} from 'lucide-react';
import { ClipboardGetText, ClipboardSetText, WindowToggleMaximise } from '../../wailsjs/runtime/runtime';

const CLEAR_FILTER_LABEL = '清除筛选';

type MenuType = 'scan' | 'sort' | null;
type SearchEditAction = 'cut' | 'copy' | 'paste' | 'selectAll' | 'undo' | 'redo';
type SearchContextMenuState = {
    x: number;
    y: number;
    hasSelection: boolean;
    hasValue: boolean;
} | null;

type SortOption = {
    field: string;
    label: string;
};

interface TopBarProps {
    hidden?: boolean;
    title: string;
    stats?: string;
    filterKind?: string;
    filterValue?: string;
    showSearch?: boolean;
    searchValue: string;
    onSearch: (keyword: string) => void;
    searchPlaceholder?: string;
    searchDisabled?: boolean;
    compactSearch?: boolean;
    scanDisabled?: boolean;
    onScanWithMode?: (mode: string) => void;
    onRandomPlay?: () => void;
    onSortSelect?: (field: string) => void;
    sortField?: string;
    sortOrder?: 'asc' | 'desc';
    sortOptions?: SortOption[];
    onBackButtonClick?: () => void;
    backButtonLabel?: string;
    onClearFilter?: () => void;
    viewMode?: 'grid' | 'list';
    onToggleViewMode?: () => void;
    libraryName?: string;
    libraryPath?: string;
    libraryMediaCount?: number;
}

const DEFAULT_SORT_OPTIONS: SortOption[] = [
    { field: 'created_at', label: '加入日期' },
    { field: 'release_date', label: '发行日期' },
    { field: 'video_codec', label: '视频编码' },
    { field: 'last_watched', label: '观看时间' },
];

const SCAN_OPTIONS = [
    { mode: 'overwrite', label: '覆盖刷新' },
    { mode: 'delete_update', label: '删改刷新' },
    { mode: 'incremental', label: '新增刷新' },
];

// 只加不删不改的新增刷新没什么可后悔的，直接跑；另外两种按危险程度分两档确认。
type ConfirmScanMode = 'overwrite' | 'delete_update';

const SCAN_CONFIRMATIONS: Record<ConfirmScanMode, { label: string; body: string; danger: boolean }> = {
    overwrite: {
        label: '覆盖刷新',
        body: '清空这个媒体库的全部条目并重新扫描目录。已看、收藏和评分会一起丢失。',
        danger: true,
    },
    delete_update: {
        label: '删改刷新',
        body: '重新扫描目录：文件已经不在的条目会从库里移除，信息有变动的条目会被更新。已看和收藏保留。',
        danger: false,
    },
};

const SEARCH_CONTEXT_MENU_WIDTH = 196;
const SEARCH_CONTEXT_MENU_HEIGHT = 252;
const SEARCH_CONTEXT_MENU_MARGIN = 8;

const SEARCH_EDIT_ACTIONS: Array<{ action: SearchEditAction; label: string; shortcut: string }> = [
    { action: 'cut', label: '剪切', shortcut: 'Ctrl+X' },
    { action: 'copy', label: '复制', shortcut: 'Ctrl+C' },
    { action: 'paste', label: '粘贴', shortcut: 'Ctrl+V' },
    { action: 'selectAll', label: '全选', shortcut: 'Ctrl+A' },
    { action: 'undo', label: '撤销', shortcut: 'Ctrl+Z' },
    { action: 'redo', label: '重做', shortcut: 'Ctrl+Y' },
];

const getSortLabel = (field: string, sortOptions: SortOption[]) => {
    return sortOptions.find((option) => option.field === field)?.label || sortOptions[0]?.label || '加入日期';
};

const getClampedSearchMenuPosition = (x: number, y: number) => {
    const maxX = Math.max(SEARCH_CONTEXT_MENU_MARGIN, window.innerWidth - SEARCH_CONTEXT_MENU_WIDTH - SEARCH_CONTEXT_MENU_MARGIN);
    const maxY = Math.max(SEARCH_CONTEXT_MENU_MARGIN, window.innerHeight - SEARCH_CONTEXT_MENU_HEIGHT - SEARCH_CONTEXT_MENU_MARGIN);

    return {
        x: Math.max(SEARCH_CONTEXT_MENU_MARGIN, Math.min(x, maxX)),
        y: Math.max(SEARCH_CONTEXT_MENU_MARGIN, Math.min(y, maxY)),
    };
};

const getInputSelection = (input: HTMLInputElement) => {
    const start = input.selectionStart ?? input.value.length;
    const end = input.selectionEnd ?? start;

    return { start, end };
};

const readClipboardText = async () => {
    try {
        return await ClipboardGetText();
    } catch {
        return '';
    }
};

const writeClipboardText = async (text: string) => {
    try {
        return await ClipboardSetText(text);
    } catch {
        return false;
    }
};

const shouldIgnoreHeaderDoubleClick = (target: EventTarget | null) => {
    if (!(target instanceof HTMLElement)) {
        return false;
    }

    return Boolean(
        target.closest(
            '.no-drag, button, input, select, textarea, a, [role="button"], [data-no-window-toggle="true"]',
        ),
    );
};

const TopBar: React.FC<TopBarProps> = ({
    hidden = false,
    title,
    stats,
    filterKind,
    filterValue,
    showSearch = true,
    searchValue,
    onSearch,
    searchPlaceholder = '搜索媒体、演员、标签',
    searchDisabled = false,
    compactSearch = false,
    scanDisabled = false,
    onScanWithMode,
    onRandomPlay,
    onSortSelect,
    sortField = 'created_at',
    sortOrder = 'desc',
    sortOptions = DEFAULT_SORT_OPTIONS,
    onBackButtonClick,
    backButtonLabel = '返回主页',
    onClearFilter,
    viewMode = 'grid',
    onToggleViewMode,
    libraryName = '',
    libraryPath = '',
    libraryMediaCount = 0,
}) => {
    const [openMenu, setOpenMenu] = useState<MenuType>(null);
    const [confirmScanMode, setConfirmScanMode] = useState<ConfirmScanMode | null>(null);
    const [searchContextMenu, setSearchContextMenu] = useState<SearchContextMenuState>(null);
    const menuRootRef = useRef<HTMLDivElement | null>(null);
    const searchContextMenuRef = useRef<HTMLDivElement | null>(null);
    const searchInputRef = useRef<HTMLInputElement | null>(null);

    useEffect(() => {
        const handlePointerDown = (event: MouseEvent) => {
            const target = event.target as Node;

            if (!menuRootRef.current?.contains(target)) {
                setOpenMenu(null);
            }

            if (!searchContextMenuRef.current?.contains(target) && target !== searchInputRef.current) {
                setSearchContextMenu(null);
            }
        };

        const handleEscape = (event: KeyboardEvent) => {
            if (event.key === 'Escape') {
                setOpenMenu(null);
                setConfirmScanMode(null);
                setSearchContextMenu(null);
            }
        };

        document.addEventListener('mousedown', handlePointerDown);
        document.addEventListener('keydown', handleEscape);

        return () => {
            document.removeEventListener('mousedown', handlePointerDown);
            document.removeEventListener('keydown', handleEscape);
        };
    }, []);

    useEffect(() => {
        if (scanDisabled) {
            setOpenMenu(null);
            setConfirmScanMode(null);
        }
    }, [scanDisabled]);

    const handleScanModeClick = (mode: string) => {
        if (scanDisabled) {
            return;
        }
        setOpenMenu(null);
        setSearchContextMenu(null);
        if (mode === 'overwrite' || mode === 'delete_update') {
            setConfirmScanMode(mode);
            return;
        }
        onScanWithMode?.(mode);
    };

    const handleHeaderDoubleClick = (event: React.MouseEvent<HTMLDivElement>) => {
        if (shouldIgnoreHeaderDoubleClick(event.target)) {
            return;
        }

        WindowToggleMaximise();
    };

    const restoreSearchSelection = (input: HTMLInputElement, start: number, end = start) => {
        window.requestAnimationFrame(() => {
            input.focus();
            input.setSelectionRange(start, end);
        });
    };

    const updateSearchInputValue = (input: HTMLInputElement, nextValue: string, selectionStart: number, selectionEnd = selectionStart) => {
        onSearch(nextValue);
        restoreSearchSelection(input, selectionStart, selectionEnd);
    };

    const syncSearchValueFromInput = (input: HTMLInputElement) => {
        const { start, end } = getInputSelection(input);
        onSearch(input.value);
        restoreSearchSelection(input, start, end);
    };

    const openSearchContextMenuAt = (x: number, y: number) => {
        const input = searchInputRef.current;
        if (!input || searchDisabled) {
            return;
        }

        const { start, end } = getInputSelection(input);
        const position = getClampedSearchMenuPosition(x, y);

        setOpenMenu(null);
        setSearchContextMenu({
            ...position,
            hasSelection: start !== end,
            hasValue: input.value.length > 0,
        });
    };

    const handleSearchContextMenu = (event: React.MouseEvent<HTMLInputElement>) => {
        if (searchDisabled) {
            return;
        }

        event.preventDefault();
        event.stopPropagation();
        event.currentTarget.focus();
        openSearchContextMenuAt(event.clientX, event.clientY);
    };

    const handleSearchKeyDown = (event: React.KeyboardEvent<HTMLInputElement>) => {
        const isKeyboardMenuKey = event.key === 'ContextMenu' || (event.shiftKey && event.key === 'F10');
        if (searchDisabled || !isKeyboardMenuKey) {
            return;
        }

        event.preventDefault();
        const rect = event.currentTarget.getBoundingClientRect();
        openSearchContextMenuAt(rect.left + 18, rect.bottom + 6);
    };

    const handleSearchEditAction = async (action: SearchEditAction) => {
        const input = searchInputRef.current;
        if (!input || searchDisabled) {
            return;
        }

        input.focus();
        setSearchContextMenu(null);

        const { start, end } = getInputSelection(input);
        const selectedText = input.value.slice(start, end);

        if (action === 'copy') {
            if (selectedText) {
                await writeClipboardText(selectedText);
            }
            return;
        }

        if (action === 'cut') {
            if (!selectedText) {
                return;
            }

            const copied = await writeClipboardText(selectedText);
            if (copied) {
                input.focus();
                input.setSelectionRange(start, end);
                const beforeValue = input.value;
                const deleted = document.execCommand('delete');
                if (deleted || input.value !== beforeValue) {
                    syncSearchValueFromInput(input);
                } else {
                    updateSearchInputValue(input, `${input.value.slice(0, start)}${input.value.slice(end)}`, start);
                }
            }
            return;
        }

        if (action === 'paste') {
            const clipboardText = await readClipboardText();
            if (!clipboardText) {
                return;
            }

            input.focus();
            input.setSelectionRange(start, end);
            const beforeValue = input.value;
            const inserted = document.execCommand('insertText', false, clipboardText);
            if (inserted || input.value !== beforeValue) {
                syncSearchValueFromInput(input);
            } else {
                const nextValue = `${input.value.slice(0, start)}${clipboardText}${input.value.slice(end)}`;
                updateSearchInputValue(input, nextValue, start + clipboardText.length);
            }
            return;
        }

        if (action === 'selectAll') {
            input.select();
            return;
        }

        document.execCommand(action === 'undo' ? 'undo' : 'redo');
        window.requestAnimationFrame(() => syncSearchValueFromInput(input));
    };

    const currentSortLabel = getSortLabel(sortField, sortOptions);
    const SortIcon = sortOrder === 'asc' ? ArrowUpNarrowWide : ArrowDownNarrowWide;
    const hasFilterChip = Boolean(filterValue);

    return (
        <>
            <div
                className={`navi-topbar ${hidden ? 'hidden' : ''}`.trim()}
                ref={menuRootRef}
                onDoubleClick={handleHeaderDoubleClick}
            >
                {onBackButtonClick && (
                    <button
                        type="button"
                        className="navi-back-btn"
                        onClick={onBackButtonClick}
                        title={backButtonLabel}
                        aria-label={backButtonLabel}
                    >
                        <ArrowLeft size={15} />
                    </button>
                )}

                <h2 className="navi-topbar-title" title={title}>{title}</h2>

                {stats && <span className="navi-topbar-count">{stats}</span>}

                {hasFilterChip && (
                    <div className="navi-filter-chip">
                        {filterKind && <span className="navi-filter-chip-kind">{filterKind}</span>}
                        <span className="navi-filter-chip-value" title={filterValue}>{filterValue}</span>
                        {onClearFilter && (
                            <button
                                type="button"
                                className="navi-filter-chip-clear"
                                title={CLEAR_FILTER_LABEL}
                                aria-label={CLEAR_FILTER_LABEL}
                                onClick={onClearFilter}
                            >
                                <X size={13} />
                            </button>
                        )}
                    </div>
                )}

                {onClearFilter && !hasFilterChip && (
                    <button type="button" className="navi-ghost-btn" onClick={onClearFilter}>
                        {CLEAR_FILTER_LABEL}
                    </button>
                )}

                <div className="navi-topbar-actions">
                    {showSearch && (
                        <label className={`navi-search ${compactSearch ? 'compact' : ''} ${searchDisabled ? 'disabled' : ''}`.trim()}>
                            <Search size={15} strokeWidth={2} />
                            <input
                                type="text"
                                ref={searchInputRef}
                                placeholder={searchPlaceholder}
                                value={searchValue}
                                onChange={(event) => {
                                    setSearchContextMenu(null);
                                    onSearch(event.target.value);
                                }}
                                onContextMenu={handleSearchContextMenu}
                                onKeyDown={handleSearchKeyDown}
                                disabled={searchDisabled}
                            />
                        </label>
                    )}

                    {onRandomPlay && (
                        <button
                            type="button"
                            className="navi-icon-btn"
                            title="随机玩玩"
                            aria-label="随机玩玩"
                            onClick={onRandomPlay}
                        >
                            <Shuffle size={16} />
                        </button>
                    )}

                    {onSortSelect && (
                        <div className={`navi-menu-shell ${openMenu === 'sort' ? 'open' : ''}`.trim()}>
                            <button
                                type="button"
                                className="navi-icon-btn"
                                title={`按${currentSortLabel}排序`}
                                aria-label={`按${currentSortLabel}排序`}
                                onClick={() => {
                                    setSearchContextMenu(null);
                                    setOpenMenu((prev) => (prev === 'sort' ? null : 'sort'));
                                }}
                            >
                                <SortIcon size={16} />
                            </button>

                            {openMenu === 'sort' && (
                                <div className="navi-menu">
                                    {sortOptions.map((option) => {
                                        const isActive = sortField === option.field;
                                        return (
                                            <button
                                                key={option.field}
                                                type="button"
                                                className={`navi-menu-item ${isActive ? 'active' : ''}`.trim()}
                                                onClick={() => {
                                                    onSortSelect(option.field);
                                                    setOpenMenu(null);
                                                }}
                                            >
                                                <span>{option.label}</span>
                                                {isActive && (
                                                    <span className="navi-menu-meta">
                                                        {sortOrder === 'desc' ? '降序' : '升序'}
                                                    </span>
                                                )}
                                            </button>
                                        );
                                    })}
                                </div>
                            )}
                        </div>
                    )}

                    {onToggleViewMode && (
                        <button
                            type="button"
                            className="navi-icon-btn"
                            title={viewMode === 'list' ? '切换网格视图' : '切换列表视图'}
                            aria-label={viewMode === 'list' ? '切换网格视图' : '切换列表视图'}
                            aria-pressed={viewMode === 'list'}
                            onClick={onToggleViewMode}
                        >
                            {viewMode === 'list' ? <LayoutGrid size={16} /> : <List size={16} />}
                        </button>
                    )}

                    {onScanWithMode && (
                        <div className={`navi-menu-shell ${openMenu === 'scan' ? 'open' : ''}`.trim()}>
                            <button
                                type="button"
                                className="navi-icon-btn"
                                title="重新扫描"
                                aria-label="重新扫描"
                                disabled={scanDisabled}
                                onClick={() => {
                                    setSearchContextMenu(null);
                                    setOpenMenu((prev) => (prev === 'scan' ? null : 'scan'));
                                }}
                            >
                                <RefreshCw size={16} />
                            </button>

                            {openMenu === 'scan' && (
                                <div className="navi-menu">
                                    {SCAN_OPTIONS.map((option) => (
                                        <button
                                            key={option.mode}
                                            type="button"
                                            className="navi-menu-item"
                                            disabled={scanDisabled}
                                            onClick={() => handleScanModeClick(option.mode)}
                                        >
                                            <span>{option.label}</span>
                                        </button>
                                    ))}
                                </div>
                            )}
                        </div>
                    )}
                </div>
            </div>

            {searchContextMenu && (
                <div
                    ref={searchContextMenuRef}
                    className="workspace-search-context-menu"
                    style={{ left: searchContextMenu.x, top: searchContextMenu.y }}
                    role="menu"
                    aria-label="搜索编辑菜单"
                    onContextMenu={(event) => event.preventDefault()}
                >
                    {SEARCH_EDIT_ACTIONS.map((item) => {
                        const disabled =
                            searchDisabled ||
                            ((item.action === 'cut' || item.action === 'copy') && !searchContextMenu.hasSelection) ||
                            (item.action === 'selectAll' && !searchContextMenu.hasValue);

                        return (
                            <button
                                key={item.action}
                                type="button"
                                className="workspace-search-context-item"
                                role="menuitem"
                                disabled={disabled}
                                onMouseDown={(event) => event.preventDefault()}
                                onClick={() => void handleSearchEditAction(item.action)}
                            >
                                <span>{item.label}</span>
                                <kbd>{item.shortcut}</kbd>
                            </button>
                        );
                    })}
                </div>
            )}

            {confirmScanMode && (
                <div className="modal-overlay" onClick={() => setConfirmScanMode(null)}>
                    <div
                        className="confirm-modal"
                        onClick={(event) => event.stopPropagation()}
                        role="dialog"
                        aria-modal="true"
                        aria-labelledby="scan-confirm-title"
                    >
                        <div className="confirm-modal-header">
                            <span id="scan-confirm-title">
                                {SCAN_CONFIRMATIONS[confirmScanMode].label}「{libraryName || title}」
                            </span>
                            <button
                                type="button"
                                className="confirm-modal-close"
                                onClick={() => setConfirmScanMode(null)}
                                aria-label="关闭"
                            >
                                <X size={14} />
                            </button>
                        </div>

                        <div className="confirm-modal-body">
                            <div className="confirm-modal-text">
                                {SCAN_CONFIRMATIONS[confirmScanMode].body}
                            </div>
                            <div className="confirm-modal-target">
                                <Folder size={12} />
                                <span className="confirm-modal-target-path" title={libraryPath}>{libraryPath}</span>
                                <span className="confirm-modal-target-count">{libraryMediaCount.toLocaleString()} 部</span>
                            </div>
                        </div>

                        <div className="confirm-modal-actions">
                            <button type="button" className="confirm-modal-btn ghost" onClick={() => setConfirmScanMode(null)}>
                                取消
                            </button>
                            <button
                                type="button"
                                className={`confirm-modal-btn ${SCAN_CONFIRMATIONS[confirmScanMode].danger ? 'danger' : 'primary'}`}
                                disabled={scanDisabled}
                                onClick={() => {
                                    if (scanDisabled) {
                                        return;
                                    }
                                    onScanWithMode?.(confirmScanMode);
                                    setConfirmScanMode(null);
                                }}
                            >
                                {SCAN_CONFIRMATIONS[confirmScanMode].label}
                            </button>
                        </div>
                    </div>
                </div>
            )}
        </>
    );
};

export default TopBar;
