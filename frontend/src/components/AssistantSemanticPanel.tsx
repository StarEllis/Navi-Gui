import React, { useEffect, useState } from 'react';
import { AlertTriangle, Check, Eye, EyeOff, LoaderCircle, Plug, RefreshCw, Sparkles, Square } from 'lucide-react';
import { AssistantBuildIndex, AssistantStopIndex, AssistantTestEmbedding } from '../../wailsjs/go/main/App';
import { assistant } from '../../wailsjs/go/models';

// 和后端 DefaultEmbeddingBaseURL / DefaultEmbeddingModel 一致：没填就用这两个。
export const DEFAULT_EMBEDDING_BASE_URL = 'https://api.siliconflow.cn/v1';
export const DEFAULT_EMBEDDING_MODEL = 'BAAI/bge-m3';

const formatError = (error: unknown) => {
    if (error instanceof Error && error.message) return error.message;
    if (typeof error === 'string' && error) return error;
    return '未知错误';
};

const formatTime = (ms: number) => {
    if (!ms) return '';
    const date = new Date(ms);
    const pad = (n: number) => String(n).padStart(2, '0');
    return `${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`;
};

type IndexPhase = 'off' | 'empty' | 'mismatch' | 'building' | 'pending' | 'ready';

// indexPhase 把状态归成一种，左边列表和右边卡片用同一套说法。
export const indexPhase = (embedding: assistant.EmbeddingConfig, status: assistant.IndexStatus | null): IndexPhase => {
    if (status?.building) return 'building';
    if (!embedding.api_key?.trim()) return 'off';
    if (!status || !status.model) return 'empty';
    if (status.model_mismatch) return 'mismatch';
    if (status.pending > 0) return 'pending';
    return 'ready';
};

export const indexPhaseLabel = (phase: IndexPhase, status: assistant.IndexStatus | null) => {
    switch (phase) {
        case 'off': return '未开启';
        case 'empty': return '还没建坐标';
        case 'mismatch': return '模型换了，要重建';
        case 'building': return `建立中 ${status?.done || 0}/${status?.todo || 0}`;
        case 'pending': return `${status?.pending || 0} 部待更新`;
        default: return `${status?.indexed || 0} 部已就绪`;
    }
};

interface Props {
    embedding: assistant.EmbeddingConfig;
    onChange: (patch: Partial<assistant.EmbeddingConfig>) => void;
    status: assistant.IndexStatus | null;
    statusError: string;
    dirty: boolean; // 有没保存的改动时不能建：建坐标用的是存下来的设置
    onBuildStarted: () => void; // 后台开始建了：马上切成"建立中"，不用等第一批进度
}

const AssistantSemanticPanel: React.FC<Props> = ({ embedding, onChange, status, statusError, dirty, onBuildStarted }) => {
    const [showKey, setShowKey] = useState(false);
    const [testing, setTesting] = useState(false);
    const [testResult, setTestResult] = useState<{ ok: boolean; text: string } | null>(null);
    const [starting, setStarting] = useState(false);
    const [actionError, setActionError] = useState('');

    // 开始建或建完了，上一次点按钮留下的提示就过时了。
    useEffect(() => {
        setActionError('');
    }, [status?.building]);

    const phase = indexPhase(embedding, status);
    const model = embedding.model?.trim() || DEFAULT_EMBEDDING_MODEL;
    const total = status?.total || 0;
    const building = phase === 'building';
    const progress = building
        ? (status?.todo ? (status.done / status.todo) : 0)
        : (total ? (status?.indexed || 0) / total : 0);

    const update = (patch: Partial<assistant.EmbeddingConfig>) => {
        setTestResult(null);
        onChange(patch);
    };

    const testConnection = async () => {
        setTesting(true);
        setTestResult(null);
        try {
            const dims = await AssistantTestEmbedding(assistant.EmbeddingConfig.createFrom(embedding));
            setTestResult({ ok: true, text: `连通了，每部片会换成 ${dims} 个数字` });
        } catch (error) {
            setTestResult({ ok: false, text: formatError(error) });
        } finally {
            setTesting(false);
        }
    };

    const build = async () => {
        setStarting(true);
        setActionError('');
        try {
            await AssistantBuildIndex();
            onBuildStarted();
        } catch (error) {
            setActionError(formatError(error));
        } finally {
            setStarting(false);
        }
    };

    const buildLabel = phase === 'mismatch'
        ? '用新模型重建'
        : phase === 'pending'
            ? `更新 ${status?.pending || 0} 部`
            : phase === 'ready' ? '已是最新' : '建立坐标';

    return (
        <div className="asst-preset-editor asst-semantic">
            <div className="asst-semantic-intro">
                <span className="asst-semantic-icon"><Sparkles size={16} /></span>
                <div>
                    <strong>按意思找片</strong>
                    <p>把每部片的标题、标签和简介换成一串坐标存在本机。按剧情找片时比的是意思，搜「温泉旅行」也能找到简介里写着「入住箱根的旅馆」的片。</p>
                </div>
            </div>

            <label className="asst-field">
                <span>接口地址</span>
                <input
                    className="asst-input mono"
                    value={embedding.base_url}
                    placeholder={`留空使用硅基流动 ${DEFAULT_EMBEDDING_BASE_URL}`}
                    onChange={(event) => update({ base_url: event.target.value })}
                />
            </label>

            <label className="asst-field">
                <span>API Key</span>
                <div className="asst-input-row">
                    <input
                        className="asst-input mono"
                        type={showKey ? 'text' : 'password'}
                        value={embedding.api_key}
                        autoComplete="off"
                        placeholder="填了才会开启这个功能"
                        onChange={(event) => update({ api_key: event.target.value })}
                    />
                    <button type="button" className="asst-icon-btn" title={showKey ? '隐藏' : '显示'} onClick={() => setShowKey((show) => !show)}>
                        {showKey ? <EyeOff size={15} /> : <Eye size={15} />}
                    </button>
                </div>
            </label>

            <label className="asst-field">
                <span>向量/嵌入模型</span>
                <input
                    className="asst-input mono"
                    value={embedding.model}
                    placeholder={`留空使用 ${DEFAULT_EMBEDDING_MODEL}（中日文都支持）`}
                    onChange={(event) => update({ model: event.target.value })}
                />
            </label>

            <div className="asst-semantic-test">
                <button
                    type="button"
                    className="asst-btn"
                    disabled={testing || !embedding.api_key?.trim()}
                    title={embedding.api_key?.trim() ? '发一句话试试接口通不通' : '先填 API Key'}
                    onClick={() => void testConnection()}
                >
                    {testing ? <LoaderCircle size={14} className="spin" /> : <Plug size={14} />}
                    {testing ? '测试中…' : '测试连接'}
                </button>
                {testResult && (
                    <span className={`asst-semantic-test-result ${testResult.ok ? 'ok' : 'error'}`}>
                        {testResult.ok ? <Check size={13} /> : <AlertTriangle size={13} />}
                        {testResult.text}
                    </span>
                )}
            </div>

            <div className={`asst-index-card ${phase}`}>
                <div className="asst-index-head">
                    <span className="asst-index-title">片库坐标</span>
                    <span className={`asst-index-pill ${phase}`}>
                        {building && <LoaderCircle size={11} className="spin" />}
                        {indexPhaseLabel(phase, status)}
                    </span>
                </div>

                <div className="asst-index-count">
                    {building ? (
                        <>
                            <strong>{status?.done || 0}</strong>
                            <span>/ {status?.todo || 0} 部，这次要建的</span>
                        </>
                    ) : (
                        <>
                            <strong>{phase === 'mismatch' ? 0 : (status?.indexed || 0)}</strong>
                            <span>/ {total} 部有坐标</span>
                        </>
                    )}
                </div>
                <div className="asst-index-bar">
                    <span style={{ width: `${Math.round(Math.min(1, progress) * 100)}%` }} />
                </div>

                {status?.model && !building && (
                    <div className="asst-index-meta">
                        {status.model}
                        {status.dims > 0 && ` · ${status.dims} 维`}
                        {status.updated_at > 0 && ` · 更新于 ${formatTime(status.updated_at)}`}
                    </div>
                )}

                {phase === 'mismatch' && (
                    <div className="asst-index-warn">
                        <AlertTriangle size={13} />
                        现有坐标是用 {status?.model} 建的，现在填的是 {model}。不同模型的坐标不能混着比，重建之前助手不会按意思找片。
                    </div>
                )}
                {(statusError || actionError || (!building && status?.error)) && (
                    <div className="asst-index-error">{statusError || actionError || status?.error}</div>
                )}

                <div className="asst-index-actions">
                    {building ? (
                        <button type="button" className="asst-btn" onClick={() => void AssistantStopIndex()}>
                            <Square size={11} fill="currentColor" />
                            停止
                        </button>
                    ) : (
                        <button
                            type="button"
                            className="asst-btn primary"
                            disabled={starting || dirty || phase === 'off' || phase === 'ready'}
                            onClick={() => void build()}
                        >
                            {starting ? <LoaderCircle size={14} className="spin" /> : <RefreshCw size={14} />}
                            {buildLabel}
                        </button>
                    )}
                    <span className="asst-index-hint">
                        {dirty
                            ? '先保存设置，再建坐标'
                            : phase === 'off'
                                ? '填好 API Key 并保存后就能建'
                                : building
                                    ? '可以关掉这个窗口，在后台接着建，助手照常能用'
                                    : '只建新加的和简介改过的，建好的不会重复花钱'}
                    </span>
                </div>
            </div>
        </div>
    );
};

export default AssistantSemanticPanel;
