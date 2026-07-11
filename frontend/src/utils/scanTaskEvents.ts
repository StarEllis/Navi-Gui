export const SCAN_EVENT_NAMES = [
    'scan:start',
    'scan:progress',
    'scan:completed',
    'scan:incomplete',
    'scan:failed',
    'scan:canceled',
] as const;

export type ScanEventName = typeof SCAN_EVENT_NAMES[number];
export type ScanEventHandler = (data: unknown) => void;
export type ScanEventSubscribe = (event: string, handler: ScanEventHandler) => () => void;

export type ScanTaskLifecycle = {
    generations: Map<string, number>;
    activeTaskIDs: Map<string, string>;
    activeGenerations: Map<string, number>;
    terminalTaskIDs: Map<string, Set<string>>;
};

export const createScanTaskLifecycle = (): ScanTaskLifecycle => ({
    generations: new Map(),
    activeTaskIDs: new Map(),
    activeGenerations: new Map(),
    terminalTaskIDs: new Map(),
});

export const beginScanRequest = (state: ScanTaskLifecycle, libraryId: string): number => {
    const generation = (state.generations.get(libraryId) || 0) + 1;
    state.generations.set(libraryId, generation);
    return generation;
};

const terminalSet = (state: ScanTaskLifecycle, libraryId: string): Set<string> => {
    let taskIDs = state.terminalTaskIDs.get(libraryId);
    if (!taskIDs) {
        taskIDs = new Set();
        state.terminalTaskIDs.set(libraryId, taskIDs);
    }
    return taskIDs;
};

export const activateScanTaskFromEvent = (state: ScanTaskLifecycle, data: any): boolean => {
    const { libraryId, taskId } = scanEventIdentity(data);
    if (!libraryId || !taskId || terminalSet(state, libraryId).has(taskId)) {
        return false;
    }
    const activeTaskId = state.activeTaskIDs.get(libraryId);
	const generation = state.generations.get(libraryId) || 0;
	if (activeTaskId && activeTaskId !== taskId) {
		const activeGeneration = state.activeGenerations.get(libraryId) || 0;
		if (activeGeneration >= generation) {
			return false;
		}
    }
    state.activeTaskIDs.set(libraryId, taskId);
	state.activeGenerations.set(libraryId, generation);
    return true;
};

export const activateScanTaskFromResponse = (
    state: ScanTaskLifecycle,
    libraryId: string,
    generation: number,
    taskId: string,
): boolean => {
    if (!taskId || state.generations.get(libraryId) !== generation || terminalSet(state, libraryId).has(taskId)) {
        return false;
    }
    const activeTaskId = state.activeTaskIDs.get(libraryId);
    if (activeTaskId && activeTaskId !== taskId) {
        return false;
    }
    state.activeTaskIDs.set(libraryId, taskId);
	state.activeGenerations.set(libraryId, generation);
    return true;
};

export const completeScanTask = (state: ScanTaskLifecycle, data: any): boolean => {
    const { libraryId, taskId } = scanEventIdentity(data);
    if (!libraryId || !taskId) {
        return false;
    }
    terminalSet(state, libraryId).add(taskId);
    if (state.activeTaskIDs.get(libraryId) !== taskId) {
        return false;
    }
    state.activeTaskIDs.delete(libraryId);
	state.activeGenerations.delete(libraryId);
    return true;
};

export const clearScanTaskLifecycle = (state: ScanTaskLifecycle): void => {
    state.generations.clear();
    state.activeTaskIDs.clear();
	state.activeGenerations.clear();
    state.terminalTaskIDs.clear();
};

export const scanEventIdentity = (data: any): { libraryId: string; taskId: string } => ({
    libraryId: typeof data?.library_id === 'string' ? data.library_id : '',
    taskId: typeof data?.task_id === 'string' ? data.task_id : '',
});

export const isCurrentScanEvent = (activeTaskId: string | undefined, data: any): boolean => {
    const { taskId } = scanEventIdentity(data);
    return taskId !== '' && activeTaskId === taskId;
};

export const canActivateScanTask = (activeTaskId: string | undefined, data: any): boolean => {
    const { taskId } = scanEventIdentity(data);
    return taskId !== '' && (!activeTaskId || activeTaskId === taskId);
};

export const scanTerminalEndsLoading = (event: string): boolean => (
    event === 'scan:completed'
    || event === 'scan:incomplete'
    || event === 'scan:failed'
    || event === 'scan:canceled'
);

export const registerScanEventListeners = (
    subscribe: ScanEventSubscribe,
    handlers: Partial<Record<ScanEventName, ScanEventHandler>>,
): (() => void) => {
    const unsubscribers = SCAN_EVENT_NAMES
        .map((event) => handlers[event] ? subscribe(event, handlers[event] as ScanEventHandler) : null)
        .filter((unsubscribe): unsubscribe is () => void => typeof unsubscribe === 'function');
    let released = false;
    return () => {
        if (released) {
            return;
        }
        released = true;
        unsubscribers.forEach((unsubscribe) => unsubscribe());
    };
};
