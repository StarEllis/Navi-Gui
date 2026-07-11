export type ScanProgressState = {
    taskId: string;
    libraryId: string;
    libraryName: string;
    mode: string;
    phase: string;
    current: number;
    total: number;
    message: string;
};

type Listener = () => void;
export type ScanLibraryProgressState = {
    active: ScanProgressState | null;
    recentTerminal: ScanProgressState | null;
};

const MAX_RETAINED_TERMINAL_LIBRARIES = 16;

export const createScanProgressStore = () => {
    const state = new Map<string, ScanLibraryProgressState>();
    const terminalOrder: string[] = [];
    const listeners = new Set<Listener>();
    const notify = () => listeners.forEach((listener) => listener());
    const retainTerminal = (libraryId: string) => {
        const previousIndex = terminalOrder.indexOf(libraryId);
        if (previousIndex >= 0) {
            terminalOrder.splice(previousIndex, 1);
        }
        terminalOrder.push(libraryId);
        while (terminalOrder.length > MAX_RETAINED_TERMINAL_LIBRARIES) {
            const evicted = terminalOrder.shift();
            if (evicted && !state.get(evicted)?.active) {
                state.delete(evicted);
            }
        }
    };
    return {
        getSnapshot: (libraryId: string) => state.get(libraryId)?.active || null,
        getLibraryState: (libraryId: string): ScanLibraryProgressState => state.get(libraryId) || { active: null, recentTerminal: null },
        subscribe: (listener: Listener) => {
            listeners.add(listener);
            return () => listeners.delete(listener);
        },
        set: (next: ScanProgressState) => {
            const libraryId = next.libraryId;
            if (!libraryId) {
                throw new Error('scan progress updates require a library ID');
            }
            const current = state.get(libraryId)?.active || null;
            if (next === current) {
                return;
            }
            state.set(libraryId, { active: next, recentTerminal: state.get(libraryId)?.recentTerminal || null });
            notify();
        },
        update: (libraryId: string, updater: (current: ScanProgressState | null) => ScanProgressState) => {
            if (!libraryId) {
                return;
            }
            const current = state.get(libraryId)?.active || null;
            const next = updater(current);
            state.set(libraryId, { active: next, recentTerminal: state.get(libraryId)?.recentTerminal || null });
            notify();
        },
        complete: (terminal: ScanProgressState) => {
            if (!terminal.libraryId) {
                return;
            }
            state.set(terminal.libraryId, { active: null, recentTerminal: terminal });
            retainTerminal(terminal.libraryId);
            notify();
        },
        clearHistory: (libraryId: string) => {
            const entry = state.get(libraryId);
            if (!entry?.recentTerminal) {
                return;
            }
            const index = terminalOrder.indexOf(libraryId);
            if (index >= 0) {
                terminalOrder.splice(index, 1);
            }
            if (entry.active) {
                state.set(libraryId, { active: entry.active, recentTerminal: null });
            } else {
                state.delete(libraryId);
            }
            notify();
        },
        clear: () => {
            if (state.size === 0) {
                return;
            }
            state.clear();
            terminalOrder.splice(0);
            notify();
        },
    };
};

export const scanProgressStore = createScanProgressStore();
