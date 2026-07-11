export const putBoundedScrollState = (
    current: Record<string, number>,
    key: string,
    scrollTop: number,
    maxEntries = 32,
) => {
    if (!key) {
        return current;
    }
    const normalized = Number.isFinite(scrollTop) ? Math.max(0, scrollTop) : 0;
    if (current[key] === normalized) {
        return current;
    }
    const next = { ...current };
    delete next[key];
    next[key] = normalized;
    const keys = Object.keys(next);
    keys.slice(0, Math.max(0, keys.length - maxEntries)).forEach((oldestKey) => delete next[oldestKey]);
    return next;
};

