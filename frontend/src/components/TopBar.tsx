import React, { useEffect, useRef, useState } from 'react';
import {
    ArrowDown,
    ArrowLeft,
    ArrowUp,
    ChevronDown,
    RefreshCw,
    Search,
    Shuffle,
} from 'lucide-react';
import { ClipboardGetText, ClipboardSetText, WindowToggleMaximise } from '../../wailsjs/runtime/runtime';

const CLEAR_FILTER_LABEL = '\u6e05\u9664\u7b5b\u9009';

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
    currentLibraryName: string;
    mediaCount: number;
    hidden?: boolean;
    filterLabel?: string;
    showSearch?: boolean;
    searchValue: string;
    onSearch: (keyword: string) => void;
    searchPlaceholder?: string;
    searchDisabled?: boolean;
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

const SEARCH_CONTEXT_MENU_WIDTH = 196;
const SEARCH_CONTEXT_MENU_HEIGHT = 252;
const SEARCH_CONTEXT_MENU_MARGIN = 8;

const SEARCH_EDIT_ACTIONS: Array<{ action: SearchEditAction; label: string; shortcut: string }> = [
    { action: 'cut', label: '\u526a\u5207', shortcut: 'Ctrl+X' },
    { action: 'copy', label: '\u590d\u5236', shortcut: 'Ctrl+C' },
    { action: 'paste', label: '\u7c98\u8d34', shortcut: 'Ctrl+V' },
    { action: 'selectAll', label: '\u5168\u9009', shortcut: 'Ctrl+A' },
    { action: 'undo', label: '\u64a4\u9500', shortcut: 'Ctrl+Z' },
    { action: 'redo', label: '\u91cd\u505a', shortcut: 'Ctrl+Y' },
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
    currentLibraryName,
    mediaCount,
    hidden = false,
    filterLabel,
    showSearch = true,
    searchValue,
    onSearch,
    searchPlaceholder = '\u641c\u7d22\u5a92\u4f53\u3001\u6f14\u5458\u3001\u6807\u7b7e',
    searchDisabled = false,
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
}) => {
    const [openMenu, setOpenMenu] = useState<MenuType>(null);
    const [confirmScanMode, setConfirmScanMode] = useState<'overwrite' | null>(null);
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
        if (searchInputRef.current) {
            searchInputRef.current.placeholder = searchPlaceholder;
        }
    }, [searchPlaceholder]);

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
        if (mode === 'overwrite') {
            setConfirmScanMode('overwrite');
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

    const countLabel = `${mediaCount.toLocaleString()} 个项目`;
    const headerHint = filterLabel || '';
    const currentSortLabel = getSortLabel(sortField, sortOptions);
    const showClearAction = Boolean(onClearFilter);

    return (
        <>
            <div className={`topbar ${hidden ? 'topbar-hidden' : ''}`} ref={menuRootRef} onDoubleClick={handleHeaderDoubleClick}>
                <div className={`workspace-header-main ${showSearch ? '' : 'no-search'} ${onBackButtonClick ? 'has-back-action' : ''}`.trim()}>
                    <div className="workspace-header-heading">
                        <div className="workspace-header-title-row">
                            <span className="workspace-library-current" title={currentLibraryName}>
                                {currentLibraryName}
                            </span>
                            <span className="workspace-library-count">{countLabel}</span>
                        </div>
                        {headerHint && <div className="workspace-header-subtitle">{headerHint}</div>}
                    </div>

                    {showSearch && <div className="workspace-header-search no-drag">
                        <div className="workspace-search-group">
                            <label className={`workspace-search-shell ${searchDisabled ? 'disabled' : ''} ${showClearAction ? 'has-clear' : ''}`}>
                                <span className="workspace-search-icon-wrap">
                                    <Search size={15} strokeWidth={2} className="workspace-search-icon" />
                                </span>
                                <input
                                    type="text"
                                    className="workspace-search" ref={searchInputRef}
                                    placeholder="搜索媒体、演员、标签"
                                    value={searchValue}
                                    onChange={(event) => {
                                        setSearchContextMenu(null);
                                        onSearch(event.target.value);
                                    }}
                                    onContextMenu={handleSearchContextMenu}
                                    onKeyDown={handleSearchKeyDown}
                                    disabled={searchDisabled}
                                />
                                {showClearAction && (
                                    <button
                                        type="button"
                                        className="workspace-search-clear"
                                        aria-label={CLEAR_FILTER_LABEL}
                                        onClick={() => onClearFilter?.()}
                                    >
                                        {CLEAR_FILTER_LABEL}
                                    </button>
                                )}
                            </label>
                        </div>
                    </div>}

                    <div className="workspace-header-actions no-drag">

                        {onBackButtonClick && (
                            <button
                                type="button"
                                className="workspace-action-btn subtle workspace-back-action"
                                onClick={onBackButtonClick}
                                title={backButtonLabel}
                                aria-label={backButtonLabel}
                            >
                                <ArrowLeft size={14} />
                                <span>{backButtonLabel}</span>
                            </button>
                        )}

                        {onRandomPlay && (
                            <button type="button" className="workspace-action-btn" onClick={onRandomPlay}>
                                <Shuffle size={15} />
                                <span>随机玩玩</span>
                            </button>
                        )}

                        {onSortSelect && (
                            <div className={`workspace-menu-shell ${openMenu === 'sort' ? 'open' : ''}`}>
                                <button
                                    type="button"
                                    className="workspace-action-btn"
                                    onClick={() => {
                                        setSearchContextMenu(null);
                                        setOpenMenu((prev) => (prev === 'sort' ? null : 'sort'));
                                    }}
                                >
                                    {sortOrder === 'asc' ? <ArrowUp size={15} /> : <ArrowDown size={15} />}
                                    <span>{`按${currentSortLabel}排序`}</span>
                                </button>

                                {openMenu === 'sort' && (
                                    <div className="workspace-dropdown-menu">
                                        {sortOptions.map((option) => {
                                            const isActive = sortField === option.field;
                                            return (
                                                <button
                                                    key={option.field}
                                                    type="button"
                                                    className={`workspace-dropdown-item ${isActive ? 'active' : ''}`}
                                                    onClick={() => {
                                                        onSortSelect(option.field);
                                                        setOpenMenu(null);
                                                    }}
                                                >
                                                    <span>{option.label}</span>
                                                    {isActive && (
                                                        <span className="workspace-dropdown-meta">
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

                        {onScanWithMode && (
                            <div className={`workspace-menu-shell ${openMenu === 'scan' ? 'open' : ''}`}>
                                <button
                                    type="button"
                                    className="workspace-action-btn compact"
                                    disabled={scanDisabled}
                                    onClick={() => {
                                        setSearchContextMenu(null);
                                        setOpenMenu((prev) => (prev === 'scan' ? null : 'scan'));
                                    }}
                                >
                                    <RefreshCw size={15} />
                                    <span>刷新</span>
                                    <ChevronDown size={13} />
                                </button>

                                {openMenu === 'scan' && (
                                    <div className="workspace-dropdown-menu">
                                        {SCAN_OPTIONS.map((option) => (
                                            <button
                                                key={option.mode}
                                                type="button"
                                                className="workspace-dropdown-item"
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
            </div>

            {searchContextMenu && (
                <div
                    ref={searchContextMenuRef}
                    className="workspace-search-context-menu"
                    style={{ left: searchContextMenu.x, top: searchContextMenu.y }}
                    role="menu"
                    aria-label="\u641c\u7d22\u7f16\u8f91\u83dc\u5355"
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

            {confirmScanMode === 'overwrite' && (
                <div className="modal-overlay" onClick={() => setConfirmScanMode(null)}>
                    <div
                        className="confirm-modal"
                        onClick={(event) => event.stopPropagation()}
                        role="dialog"
                        aria-modal="true"
                        aria-labelledby="overwrite-confirm-title"
                    >
                        <div className="confirm-modal-header">
                            <span id="overwrite-confirm-title">提示</span>
                            <button
                                type="button"
                                className="confirm-modal-close"
                                onClick={() => setConfirmScanMode(null)}
                                aria-label="关闭"
                            >
                                ×
                            </button>
                        </div>

                        <div className="confirm-modal-body">
                            <div className="confirm-modal-icon">!</div>
                            <div className="confirm-modal-text">
                                你确定要覆盖刷新吗？这会清空当前媒体库并重新扫描。
                            </div>
                        </div>

                        <div className="confirm-modal-actions">
                            <button type="button" className="confirm-modal-btn ghost" onClick={() => setConfirmScanMode(null)}>
                                取消
                            </button>
                            <button
                                type="button"
                                className="confirm-modal-btn primary"
                                disabled={scanDisabled}
                                onClick={() => {
                                    if (scanDisabled) {
                                        return;
                                    }
                                    onScanWithMode?.('overwrite');
                                    setConfirmScanMode(null);
                                }}
                            >
                                确定
                            </button>
                        </div>
                    </div>
                </div>
            )}
        </>
    );
};

export default TopBar;
