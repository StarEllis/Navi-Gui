import React, { useEffect, useState } from 'react';
import { FolderIcon, FolderPlusIcon, XIcon } from 'lucide-react';
import { CreateLibrary, DeleteLibrary, SelectDirectory, UpdateLibrary } from "../../wailsjs/go/main/App";
import {
    buildLibraryPayload,
    DEFAULT_LIBRARY_SUBTITLE_FIELD,
    DEFAULT_LIBRARY_TITLE_FIELD,
    DEFAULT_LIBRARY_VIEW_MODE,
    getLibraryConfig,
} from '../utils/library';
import { applyLibraryDeleteOutcome } from '../utils/libraryDeleteOutcome';

interface LibraryFormModalProps {
    mode: 'create' | 'edit';
    library?: any;
    onClose: () => void;
    onSaved: (library?: any) => void;
    onDeleted?: () => void;
}

const emptyConfig = {
    folderPaths: [] as string[],
    viewMode: DEFAULT_LIBRARY_VIEW_MODE,
    titleField: DEFAULT_LIBRARY_TITLE_FIELD,
    subtitleField: DEFAULT_LIBRARY_SUBTITLE_FIELD,
};

const LibraryFormModal: React.FC<LibraryFormModalProps> = ({
    mode,
    library,
    onClose,
    onSaved,
    onDeleted,
}) => {
    const [name, setName] = useState('');
    const [folderPaths, setFolderPaths] = useState<string[]>([]);
    const [manualPath, setManualPath] = useState('');
    const [viewMode, setViewMode] = useState(DEFAULT_LIBRARY_VIEW_MODE);
    const [titleField, setTitleField] = useState(DEFAULT_LIBRARY_TITLE_FIELD);
    const [subtitleField, setSubtitleField] = useState(DEFAULT_LIBRARY_SUBTITLE_FIELD);
    const [nameTouched, setNameTouched] = useState(false);
    const [attempted, setAttempted] = useState(false);
    const [msg, setMsg] = useState('');

    const resetForm = () => {
        const config = library ? getLibraryConfig(library) : emptyConfig;
        setName(library?.name || '');
        setFolderPaths(config.folderPaths);
        setViewMode(config.viewMode);
        setTitleField(config.titleField);
        setSubtitleField(config.subtitleField);
        setManualPath('');
        setNameTouched(false);
        setAttempted(false);
        setMsg('');
    };

    useEffect(() => {
        resetForm();
    }, [library, mode]);

    const pushPath = (path: string) => {
        const trimmed = path.trim();
        if (!trimmed) {
            return;
        }
        setFolderPaths((prev) => (prev.includes(trimmed) ? prev : [...prev, trimmed]));
        setManualPath('');
    };

    const handleSelectDir = async () => {
        try {
            const dir = await SelectDirectory();
            if (dir) {
                pushPath(dir);
            }
        } catch (error) {
            console.error(error);
        }
    };

    const handleSave = async () => {
        setAttempted(true);

        const payload = buildLibraryPayload(library || {}, {
            name: name.trim(),
            folderPaths,
            viewMode,
            titleField,
            subtitleField,
        });

        try {
            if (mode === 'create') {
                const createdLibrary = await CreateLibrary({
                    ...payload,
                    type: library?.type || 'movie',
                    metadata_mode: library?.metadata_mode || 'online_preferred',
                } as any);
                onSaved(createdLibrary);
            } else {
                await UpdateLibrary(payload as any);
                onSaved(payload);
            }
        } catch (error: any) {
            setMsg(`${mode === 'create' ? '创建' : '保存'}失败：${error}`);
        }
    };

    const handleDelete = async () => {
        if (!library || !onDeleted) {
            return;
        }
        if (!window.confirm('确定要删除此媒体库及其媒体记录吗？此操作不可撤销。')) {
            return;
        }

        try {
            const result = await DeleteLibrary(library.id);
            if (!applyLibraryDeleteOutcome(result, onDeleted, (warning) => {
                window.alert(`媒体库已删除，但缓存清理失败：${warning}`);
            })) {
                setMsg('删除失败：后端未确认媒体库已删除');
            }
        } catch (error: any) {
            setMsg(`删除失败：${error}`);
        }
    };

    const isCreateMode = mode === 'create';
    const canSave = name.trim().length > 0 && folderPaths.length > 0;

    return (
        <div className="modal-overlay" onClick={onClose}>
            <div className="library-edit-modal" onClick={(event) => event.stopPropagation()}>
                <div className="library-edit-header">
                    <span>{isCreateMode ? '新建媒体库' : '编辑媒体库'}</span>
                    <button type="button" className="library-edit-close" onClick={onClose}>×</button>
                </div>

                <div className="library-edit-body">
                    <div className="library-edit-row">
                        <label className="library-edit-label">名称</label>
                        <input
                            className="library-edit-input"
                            value={name}
                            autoFocus={isCreateMode}
                            placeholder={isCreateMode ? '给它起个名字' : undefined}
                            onChange={(event) => {
                                setName(event.target.value);
                                setNameTouched(true);
                            }}
                            onBlur={() => setNameTouched(true)}
                        />
                        {nameTouched && !name.trim() && (
                            <div className="library-edit-error">媒体库名称不能为空</div>
                        )}
                    </div>

                    <div className="library-edit-row">
                        <label className="library-edit-label">视图</label>
                        <div className="library-edit-segment">
                            <button
                                type="button"
                                aria-pressed={viewMode === 'poster'}
                                onClick={() => setViewMode('poster')}
                            >
                                海报图
                            </button>
                            <button
                                type="button"
                                aria-pressed={viewMode === 'compact'}
                                onClick={() => setViewMode('compact')}
                            >
                                紧凑图
                            </button>
                        </div>
                    </div>

                    <div className="library-edit-row">
                        <label className="library-edit-label">卡片文字</label>
                        <div className="library-edit-pickers">
                            <div className="library-edit-picker">
                                <select value={titleField} onChange={(event) => setTitleField(event.target.value)}>
                                    <option value="title">标题</option>
                                    <option value="code">视频编码</option>
                                    <option value="orig_title">原标题</option>
                                </select>
                            </div>
                            <span className="library-edit-pickers-sep">/</span>
                            <div className="library-edit-picker">
                                <select value={subtitleField} onChange={(event) => setSubtitleField(event.target.value)}>
                                    <option value="year">年份</option>
                                    <option value="release_date">发行日期</option>
                                    <option value="none">无</option>
                                </select>
                            </div>
                        </div>
                    </div>

                    <div className="library-edit-row library-edit-row--paths">
                        <label className="library-edit-label">文件夹</label>
                        <div>
                            <div className="library-path-list">
                                {folderPaths.length > 0 ? folderPaths.map((path) => (
                                    <div key={path} className="library-path-row">
                                        <FolderIcon className="icon" />
                                        <span className="path" title={path}>{path}</span>
                                        <button
                                            type="button"
                                            className="library-path-remove"
                                            title="移除"
                                            onClick={() => setFolderPaths((prev) => prev.filter((item) => item !== path))}
                                        >
                                            <XIcon size={14} />
                                        </button>
                                    </div>
                                )) : (
                                    <div className={`library-path-empty${attempted ? ' invalid' : ''}`}>
                                        还没有文件夹，至少选一个
                                    </div>
                                )}
                            </div>
                            <div className="library-edit-path-add">
                                <button type="button" className="library-edit-browse-btn" onClick={handleSelectDir}>
                                    <FolderPlusIcon size={14} />选择文件夹
                                </button>
                                <input
                                    className="library-edit-manual-input"
                                    value={manualPath}
                                    placeholder="或粘贴路径后回车"
                                    onChange={(event) => setManualPath(event.target.value)}
                                    onKeyDown={(event) => {
                                        if (event.key === 'Enter') {
                                            event.preventDefault();
                                            pushPath(manualPath);
                                        }
                                    }}
                                />
                            </div>
                        </div>
                    </div>
                </div>

                <div className="library-edit-footer">
                    {!isCreateMode
                        ? <button type="button" className="library-edit-footer-btn danger" onClick={handleDelete}>删除媒体库</button>
                        : <span />}
                    <div className="library-edit-footer-actions">
                        {msg && <span className="library-edit-msg">{msg}</span>}
                        {!isCreateMode && (
                            <button type="button" className="library-edit-footer-btn quiet" onClick={resetForm}>重置</button>
                        )}
                        <button type="button" className="library-edit-footer-btn" onClick={onClose}>取消</button>
                        <button type="button" className="library-edit-footer-btn primary" disabled={!canSave} onClick={handleSave}>
                            保存
                        </button>
                    </div>
                </div>
            </div>
        </div>
    );
};

export default LibraryFormModal;
