export type TrailerVolume = { volume: number; muted: boolean };

type TrailerVolumeStorage = Pick<Storage, 'getItem' | 'setItem'>;

const TRAILER_VOLUME_STORAGE_KEY = 'navi.desktop.trailerVolume.v1';

export const DEFAULT_TRAILER_VOLUME: TrailerVolume = { volume: 1, muted: false };

const resolveStorage = (): TrailerVolumeStorage | null => {
    if (typeof window === 'undefined' || !window.localStorage) {
        return null;
    }
    return window.localStorage;
};

export const normalizeTrailerVolume = (value: unknown): TrailerVolume => {
    const candidate = value && typeof value === 'object' ? value as Partial<TrailerVolume> : {};
    const volume = typeof candidate.volume === 'number' && Number.isFinite(candidate.volume)
        ? Math.min(1, Math.max(0, candidate.volume))
        : DEFAULT_TRAILER_VOLUME.volume;
    return { volume, muted: candidate.muted === true };
};

export const loadTrailerVolume = (
    storage: TrailerVolumeStorage | null = resolveStorage(),
): TrailerVolume => {
    if (!storage) {
        return normalizeTrailerVolume(null);
    }
    try {
        const raw = storage.getItem(TRAILER_VOLUME_STORAGE_KEY);
        return normalizeTrailerVolume(raw ? JSON.parse(raw) : null);
    } catch (_error) {
        return normalizeTrailerVolume(null);
    }
};

export const persistTrailerVolume = (
    preference: TrailerVolume,
    storage: TrailerVolumeStorage | null = resolveStorage(),
) => {
    if (!storage) {
        return false;
    }
    try {
        storage.setItem(TRAILER_VOLUME_STORAGE_KEY, JSON.stringify(normalizeTrailerVolume(preference)));
        return true;
    } catch (_error) {
        return false;
    }
};
