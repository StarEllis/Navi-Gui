import React, { useCallback, useEffect, useState } from 'react';
import { Download, LoaderCircle, TriangleAlert, X } from 'lucide-react';
import { EventsOn } from '../../wailsjs/runtime/runtime';
import { DownloadFFmpeg, GetFFmpegStatus } from '../../wailsjs/go/main/App';

type SetupPhase = 'downloading' | 'extracting' | 'verifying' | 'done' | 'failed';

type SetupProgress = {
    phase: SetupPhase;
    received: number;
    total: number;
    message: string;
};

const PHASE_LABELS: Record<SetupPhase, string> = {
    downloading: '正在下载',
    extracting: '正在解压',
    verifying: '正在验证',
    done: 'FFmpeg 已就绪',
    failed: '安装失败',
};

const formatMB = (bytes: number) => `${(bytes / 1024 / 1024).toFixed(1)} MB`;

// 缺 FFmpeg 不影响浏览和播放，只影响时长/分辨率/字幕轨和缩略图，
// 所以这里是一条提示条而不是拦路的弹窗。叉只关掉这一次——FFmpeg 是必需的，
// 下次启动还得再提醒一遍，不做"永久不再提醒"。
const FFmpegNotice: React.FC = () => {
    const [visible, setVisible] = useState(false);
    const [progress, setProgress] = useState<SetupProgress | null>(null);

    const refresh = useCallback(async () => {
        try {
            const status = await GetFFmpegStatus();
            setVisible(!status.available && status.supported);
            if (status.downloading) {
                setProgress({ phase: 'downloading', received: 0, total: 0, message: '正在下载 FFmpeg' });
            }
        } catch (_error) {
            setVisible(false);
        }
    }, []);

    useEffect(() => {
        refresh();
    }, [refresh]);

    useEffect(() => {
        const unsubscribe = EventsOn('ffmpeg:setup-progress', (data: SetupProgress) => {
            setProgress(data);
            // 装好了先让"已就绪"停一下再收起来，否则提示条一闪而过，用户不知道成没成。
            if (data?.phase === 'done') {
                window.setTimeout(() => setVisible(false), 2200);
            }
        });
        return () => unsubscribe();
    }, []);

    const handleDownload = async () => {
        setProgress({ phase: 'downloading', received: 0, total: 0, message: '正在下载 FFmpeg' });
        try {
            await DownloadFFmpeg();
        } catch (error: any) {
            setProgress({ phase: 'failed', received: 0, total: 0, message: String(error) });
        }
    };

    // 只收起当前这次，什么都不写盘，下次打开还会提示。
    const handleDismiss = () => setVisible(false);

    if (!visible) {
        return null;
    }

    const busy = progress !== null && progress.phase !== 'done' && progress.phase !== 'failed';
    const percent = progress && progress.total > 0
        ? Math.min(100, Math.round((progress.received / progress.total) * 100))
        : 0;

    return (
        <div className={`ffmpeg-notice${progress?.phase === 'failed' ? ' is-error' : ''}`}>
            <span className="ffmpeg-notice-icon">
                {busy ? <LoaderCircle size={14} className="ffmpeg-notice-spin" /> : <TriangleAlert size={14} />}
            </span>

            <div className="ffmpeg-notice-body">
                {progress ? (
                    <>
                        <span className="ffmpeg-notice-title">{PHASE_LABELS[progress.phase]}</span>
                        {progress.phase === 'downloading' && progress.total > 0 && (
                            <span className="ffmpeg-notice-detail">
                                {formatMB(progress.received)} / {formatMB(progress.total)}（{percent}%）
                            </span>
                        )}
                        {progress.phase === 'failed' && (
                            <span className="ffmpeg-notice-detail">{progress.message}</span>
                        )}
                    </>
                ) : (
                    <>
                        <span className="ffmpeg-notice-title">未检测到 FFmpeg</span>
                        <span className="ffmpeg-notice-detail">
                            时长、分辨率、字幕轨和视频缩略图将无法获取，其余功能不受影响。
                            下载约 68 MB，装好后占用约 129 MB。
                        </span>
                    </>
                )}

                {progress?.phase === 'downloading' && (
                    <div className="ffmpeg-notice-bar">
                        <div className="ffmpeg-notice-bar-fill" style={{ width: `${percent}%` }} />
                    </div>
                )}
            </div>

            {!busy && (
                <button type="button" className="ffmpeg-notice-action" onClick={handleDownload}>
                    <Download size={13} />
                    {progress?.phase === 'failed' ? '重试' : '下载并启用（约 68 MB）'}
                </button>
            )}

            <button type="button" className="ffmpeg-notice-close" onClick={handleDismiss} title="本次不再显示（下次启动仍会提醒）">
                <X size={14} />
            </button>
        </div>
    );
};

export default FFmpegNotice;
