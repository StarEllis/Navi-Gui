type PerformanceDiagnostics = {
    renders: Record<string, number>;
};

declare global {
    interface Window {
        __ALEX_PERF__?: PerformanceDiagnostics;
    }
}

export const markComponentRender = (componentName: string) => {
    if (!import.meta.env.DEV || !window.__ALEX_PERF__) {
        return;
    }

    const renders = window.__ALEX_PERF__.renders;
    renders[componentName] = (renders[componentName] || 0) + 1;
};

