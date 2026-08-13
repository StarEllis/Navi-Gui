import React, { useEffect, useRef, useState } from 'react';
import {
    Eye,
    Heart,
    Library,
    Pencil,
    Plus,
    Settings,
    Shapes,
    UserRound,
} from 'lucide-react';
import { Quit, WindowIsMaximised, WindowMinimise, WindowToggleMaximise } from '../../wailsjs/runtime/runtime';
import logoImage from '../assets/images/logo-universal.png';

interface SidebarProps {
    appName: string;
    libraries: any[];
    currentLib: any;
    currentView: string;
    onSelectLib: (lib: any) => void;
    onOpenSettings: () => void;
    onSelectView: (view: 'libs' | 'actor' | 'genre' | 'watched' | 'favorite') => void;
    onAddLib: () => void;
    onEditLib: (lib: any) => void;
}

const navItems = [
    { key: 'watched', label: '已看', icon: Eye },
    { key: 'favorite', label: '收藏', icon: Heart },
    { key: 'actor', label: '演员', icon: UserRound },
    { key: 'genre', label: '类别', icon: Shapes },
] as const;

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

const Sidebar: React.FC<SidebarProps> = ({
    appName,
    libraries,
    currentLib,
    currentView,
    onSelectLib,
    onOpenSettings,
    onSelectView,
    onAddLib,
    onEditLib,
}) => {
    const [isMaximised, setIsMaximised] = useState(false);
    const [showLibraryPicker, setShowLibraryPicker] = useState(false);
    const libraryPickerRef = useRef<HTMLDivElement | null>(null);

    useEffect(() => {
        const syncWindowState = () => {
            WindowIsMaximised().then(setIsMaximised).catch(() => undefined);
        };

        syncWindowState();
        window.addEventListener('resize', syncWindowState);

        return () => {
            window.removeEventListener('resize', syncWindowState);
        };
    }, []);

    useEffect(() => {
        if (!showLibraryPicker) {
            return;
        }

        const handlePointerDown = (event: MouseEvent) => {
            if (!libraryPickerRef.current?.contains(event.target as Node)) {
                setShowLibraryPicker(false);
            }
        };

        const handleEscape = (event: KeyboardEvent) => {
            if (event.key === 'Escape') {
                setShowLibraryPicker(false);
            }
        };

        document.addEventListener('mousedown', handlePointerDown);
        document.addEventListener('keydown', handleEscape);

        return () => {
            document.removeEventListener('mousedown', handlePointerDown);
            document.removeEventListener('keydown', handleEscape);
        };
    }, [showLibraryPicker]);

    const handleWindowToggle = () => {
        WindowToggleMaximise();
        window.setTimeout(() => {
            WindowIsMaximised().then(setIsMaximised).catch(() => undefined);
        }, 80);
    };

    const handleRailDoubleClick = (event: React.MouseEvent<HTMLElement>) => {
        if (shouldIgnoreHeaderDoubleClick(event.target)) {
            return;
        }

        handleWindowToggle();
    };

    // 媒体库：不在媒体库视图时先回到网格，已经在网格上时再点开切换库的浮层。
    const handleLibraryRailClick = () => {
        if (currentView !== 'libs') {
            setShowLibraryPicker(false);
            onSelectView('libs');
            return;
        }
        setShowLibraryPicker((open) => !open);
    };

    return (
        <aside className="navi-rail" onDoubleClick={handleRailDoubleClick}>
            <div className="navi-window-controls">
                <button type="button" className="navi-window-btn close" onClick={Quit} aria-label="关闭" />
                <button type="button" className="navi-window-btn min" onClick={WindowMinimise} aria-label="最小化" />
                <button
                    type="button"
                    className={`navi-window-btn max${isMaximised ? ' restore' : ''}`}
                    onClick={handleWindowToggle}
                    aria-label="最大化"
                />
            </div>

            <div className="navi-rail-logo" title={appName} aria-hidden="true">
                <img src={logoImage} alt="" />
            </div>

            <button
                type="button"
                className={`navi-rail-item ${currentView === 'libs' ? 'active' : ''}`.trim()}
                onClick={handleLibraryRailClick}
                title={currentLib?.name ? `媒体库：${currentLib.name}` : '媒体库'}
                aria-expanded={showLibraryPicker}
            >
                <Library size={17} strokeWidth={1.85} />
                <span>媒体库</span>
            </button>

            {navItems.map((item) => {
                const Icon = item.icon;
                return (
                    <button
                        key={item.key}
                        type="button"
                        className={`navi-rail-item ${currentView === item.key ? 'active' : ''}`.trim()}
                        onClick={() => {
                            setShowLibraryPicker(false);
                            onSelectView(item.key);
                        }}
                    >
                        <Icon size={17} strokeWidth={1.85} />
                        <span>{item.label}</span>
                    </button>
                );
            })}

            <button
                type="button"
                className={`navi-rail-item settings ${currentView === 'settings' ? 'active' : ''}`.trim()}
                onClick={() => {
                    setShowLibraryPicker(false);
                    onOpenSettings();
                }}
            >
                <Settings size={17} strokeWidth={1.85} />
                <span>设置</span>
            </button>

            {showLibraryPicker && (
                <div className="navi-library-popover" ref={libraryPickerRef} role="menu" aria-label="切换媒体库">
                    <div className="navi-library-popover-title">媒体库</div>

                    {libraries.length === 0 && (
                        <div className="navi-library-empty">还没有媒体库</div>
                    )}

                    {libraries.map((lib) => (
                        <div
                            key={lib.id}
                            className={`navi-library-row ${currentLib?.id === lib.id ? 'active' : ''}`.trim()}
                        >
                            <button
                                type="button"
                                className="navi-library-main"
                                onClick={() => {
                                    setShowLibraryPicker(false);
                                    onSelectLib(lib);
                                }}
                            >
                                <span className="navi-library-name" title={lib.name}>{lib.name}</span>
                                <span className="navi-library-count">
                                    {(lib.media_count || 0).toLocaleString()}
                                </span>
                            </button>
                            <button
                                type="button"
                                className="navi-library-edit"
                                title="编辑媒体库"
                                onClick={() => {
                                    setShowLibraryPicker(false);
                                    onEditLib(lib);
                                }}
                            >
                                <Pencil size={13} />
                            </button>
                        </div>
                    ))}

                    <button
                        type="button"
                        className="navi-library-add"
                        onClick={() => {
                            setShowLibraryPicker(false);
                            onAddLib();
                        }}
                    >
                        <Plus size={14} />
                        <span>新建媒体库</span>
                    </button>
                </div>
            )}
        </aside>
    );
};

export default Sidebar;
