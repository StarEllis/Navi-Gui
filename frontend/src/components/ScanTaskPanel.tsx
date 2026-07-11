import React, { useSyncExternalStore } from 'react';
import { markComponentRender } from '../utils/performanceDiagnostics';
import { scanProgressStore } from '../utils/scanProgressStore';

const ScanTaskPanel: React.FC<{ hidden: boolean; libraryId: string }> = ({ hidden, libraryId }) => {
    markComponentRender('ScanTaskPanel');
    const progress = useSyncExternalStore(
        scanProgressStore.subscribe,
        () => scanProgressStore.getSnapshot(libraryId),
        () => scanProgressStore.getSnapshot(libraryId),
    );
    if (hidden || !progress) {
        return null;
    }

    const progressText = `正在扫描: ${progress.current}/${progress.total > 0 ? progress.total : '...'}`;
    return (
        <div className="scan-progress-panel" role="status" aria-live="polite">
            <div className="scan-progress-title">{progressText}</div>
            {progress.message && <div className="scan-progress-message">{progress.message}</div>}
        </div>
    );
};

export default React.memo(ScanTaskPanel);
