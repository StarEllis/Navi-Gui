import { model, service } from "../../wailsjs/go/models";

export type AppSeries = Omit<model.Series, "convertValues">;

export type AppMediaActor = model.MediaActor;

export type AppMedia = Omit<model.Media, "convertValues" | "series" | "actors"> & {
    fanart_path?: string;
    publisher?: string;
    search_text?: string;
    position?: number;
    watch_duration?: number;
    progress_percent?: number;
    completed?: boolean;
    last_watched_at?: string;
    playback_state?: string;
    revision?: number;
    series?: AppSeries;
    actors?: AppMediaActor[];
};

export type RecommendationItem = Omit<service.RelatedMediaItem, "convertValues" | "media"> & {
    media: AppMedia;
};

export type RecommendationGroups = Omit<
    service.DetailRecommendationResponse,
    "convertValues" | "continue_watching" | "more_like_this"
> & {
    continue_watching: RecommendationItem[];
    more_like_this: RecommendationItem[];
};

export type NFOEditorDraft = service.NFOEditorData;

export interface MediaFilter {
    type: string;
    value: string;
    label: string;
}
