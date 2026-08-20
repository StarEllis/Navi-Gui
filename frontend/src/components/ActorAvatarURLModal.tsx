import React, { useEffect, useRef, useState } from 'react';
import { X } from 'lucide-react';

interface ActorAvatarURLModalProps {
    actorName: string;
    submitting?: boolean;
    onSubmit: (imageURL: string) => void;
    onClose: () => void;
}

const ActorAvatarURLModal: React.FC<ActorAvatarURLModalProps> = ({
    actorName,
    submitting = false,
    onSubmit,
    onClose,
}) => {
    const [imageURL, setImageURL] = useState('');
    const inputRef = useRef<HTMLInputElement | null>(null);

    useEffect(() => {
        inputRef.current?.focus();
    }, []);

    useEffect(() => {
        const handleEscape = (event: KeyboardEvent) => {
            if (event.key === 'Escape' && !submitting) {
                onClose();
            }
        };
        document.addEventListener('keydown', handleEscape);
        return () => document.removeEventListener('keydown', handleEscape);
    }, [onClose, submitting]);

    const submit = () => {
        const trimmed = imageURL.trim();
        if (trimmed && !submitting) {
            onSubmit(trimmed);
        }
    };

    return (
        <div className="modal-overlay" onClick={() => !submitting && onClose()}>
            <div
                className="confirm-modal"
                onClick={(event) => event.stopPropagation()}
                role="dialog"
                aria-modal="true"
                aria-labelledby="actor-avatar-url-title"
            >
                <div className="confirm-modal-header">
                    <span id="actor-avatar-url-title">设置「{actorName}」的头像</span>
                    <button type="button" className="confirm-modal-close" onClick={onClose} aria-label="关闭">
                        <X size={14} />
                    </button>
                </div>

                <div className="confirm-modal-body">
                    <div className="confirm-modal-text">
                        粘贴一个图片链接。图片会缩放后存进本地库，之后不再依赖这个链接。
                    </div>
                    <input
                        ref={inputRef}
                        type="text"
                        className="actor-avatar-url-input"
                        placeholder="https://example.com/actor.jpg"
                        value={imageURL}
                        disabled={submitting}
                        onChange={(event) => setImageURL(event.target.value)}
                        onKeyDown={(event) => {
                            if (event.key === 'Enter') {
                                submit();
                            }
                        }}
                    />
                </div>

                <div className="confirm-modal-actions">
                    <button type="button" className="confirm-modal-btn ghost" onClick={onClose} disabled={submitting}>
                        取消
                    </button>
                    <button
                        type="button"
                        className="confirm-modal-btn primary"
                        onClick={submit}
                        disabled={submitting || !imageURL.trim()}
                    >
                        {submitting ? '正在下载…' : '设置'}
                    </button>
                </div>
            </div>
        </div>
    );
};

export default ActorAvatarURLModal;
