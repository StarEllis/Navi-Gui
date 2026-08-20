// 预告片放在网络盘上时，点开到出画有 2-3 秒是 SMB 打开文件的往返。
// 鼠标悬停时先拉一小段把文件预热，指针移到格子上到点下去的这几百毫秒
// 就不至于白等。拉多少不重要，重要的是让链路先通。
export const ASSET_PREFETCH_BYTES = 262144;

export type AssetFetch = (url: string, init: { headers: Record<string, string> }) => unknown;

const resolveFetch = (): AssetFetch | null => {
    if (typeof window === 'undefined' || typeof window.fetch !== 'function') {
        return null;
    }
    return (url, init) => window.fetch(url, init);
};

export const createAssetPrefetcher = (fetchAsset: AssetFetch | null = resolveFetch()) => {
    const warmed = new Set<string>();

    return (url: string): boolean => {
        const target = typeof url === 'string' ? url.trim() : '';
        if (!target || !fetchAsset || warmed.has(target)) {
            return false;
        }

        warmed.add(target);
        try {
            const result = fetchAsset(target, {
                headers: { Range: `bytes=0-${ASSET_PREFETCH_BYTES - 1}` },
            }) as Promise<unknown> | undefined;
            // 预热失败不留痕迹，下次悬停还能再试一次。
            if (result && typeof result.catch === 'function') {
                result.catch(() => {
                    warmed.delete(target);
                });
            }
        } catch (_error) {
            warmed.delete(target);
            return false;
        }
        return true;
    };
};
