export namespace gorm {
	
	export class DeletedAt {
	    // Go type: time
	    Time: any;
	    Valid: boolean;
	
	    static createFrom(source: any = {}) {
	        return new DeletedAt(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Time = this.convertValues(source["Time"], null);
	        this.Valid = source["Valid"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace main {
	
	export class ActorAvatarBackfillResult {
	    filled: number;
	    pending: number;
	
	    static createFrom(source: any = {}) {
	        return new ActorAvatarBackfillResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.filled = source["filled"];
	        this.pending = source["pending"];
	    }
	}
	export class ActorAvatarCycleResult {
	    profile_url: string;
	    index: number;
	    total: number;
	
	    static createFrom(source: any = {}) {
	        return new ActorAvatarCycleResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.profile_url = source["profile_url"];
	        this.index = source["index"];
	        this.total = source["total"];
	    }
	}
	export class DeleteLibraryResult {
	    deleted: boolean;
	    warning?: string;
	
	    static createFrom(source: any = {}) {
	        return new DeleteLibraryResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.deleted = source["deleted"];
	        this.warning = source["warning"];
	    }
	}
	export class DesktopSettings {
	    player_path: string;
	    use_external_player: boolean;
	    loop_playback: boolean;
	    read_file_info: boolean;
	    theme: string;
	    poster_radius: number;
	    backdrop_blur: number;
	    min_window_width: number;
	    show_subtitle_tag: boolean;
	    show_resolution_tag: boolean;
	    show_count_tag: boolean;
	    show_genre_in_list: boolean;
	    show_series_in_list: boolean;
	    static_loading: boolean;
	    hotkey: string;
	    min_to_tray: boolean;
	    max_no_taskbar: boolean;
	    show_prompt: boolean;
	    start_with_os: boolean;
	    skip_no_nfo: boolean;
	    get_resolution: boolean;
	    use_everything: boolean;
	    everything_addr: string;
	    scan_from_video_dir: boolean;
	    enable_video_thumbnail: boolean;
	    enable_gfriends_avatars: boolean;
	    thumbnail_preview_count: number;
	    thumbnail_min_duration_seconds: number;
	    remote_bind_host: string;
	    remote_username: string;
	    remote_password: string;
	    jellyfin_enabled: boolean;
	    jellyfin_port: number;
	    jellyfin_server_name: string;
	    emby_enabled: boolean;
	    emby_user: string;
	    emby_url: string;
	    emby_api_key: string;
	
	    static createFrom(source: any = {}) {
	        return new DesktopSettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.player_path = source["player_path"];
	        this.use_external_player = source["use_external_player"];
	        this.loop_playback = source["loop_playback"];
	        this.read_file_info = source["read_file_info"];
	        this.theme = source["theme"];
	        this.poster_radius = source["poster_radius"];
	        this.backdrop_blur = source["backdrop_blur"];
	        this.min_window_width = source["min_window_width"];
	        this.show_subtitle_tag = source["show_subtitle_tag"];
	        this.show_resolution_tag = source["show_resolution_tag"];
	        this.show_count_tag = source["show_count_tag"];
	        this.show_genre_in_list = source["show_genre_in_list"];
	        this.show_series_in_list = source["show_series_in_list"];
	        this.static_loading = source["static_loading"];
	        this.hotkey = source["hotkey"];
	        this.min_to_tray = source["min_to_tray"];
	        this.max_no_taskbar = source["max_no_taskbar"];
	        this.show_prompt = source["show_prompt"];
	        this.start_with_os = source["start_with_os"];
	        this.skip_no_nfo = source["skip_no_nfo"];
	        this.get_resolution = source["get_resolution"];
	        this.use_everything = source["use_everything"];
	        this.everything_addr = source["everything_addr"];
	        this.scan_from_video_dir = source["scan_from_video_dir"];
	        this.enable_video_thumbnail = source["enable_video_thumbnail"];
	        this.enable_gfriends_avatars = source["enable_gfriends_avatars"];
	        this.thumbnail_preview_count = source["thumbnail_preview_count"];
	        this.thumbnail_min_duration_seconds = source["thumbnail_min_duration_seconds"];
	        this.remote_bind_host = source["remote_bind_host"];
	        this.remote_username = source["remote_username"];
	        this.remote_password = source["remote_password"];
	        this.jellyfin_enabled = source["jellyfin_enabled"];
	        this.jellyfin_port = source["jellyfin_port"];
	        this.jellyfin_server_name = source["jellyfin_server_name"];
	        this.emby_enabled = source["emby_enabled"];
	        this.emby_user = source["emby_user"];
	        this.emby_url = source["emby_url"];
	        this.emby_api_key = source["emby_api_key"];
	    }
	}
	export class FFmpegStatus {
	    available: boolean;
	    ffmpeg_path: string;
	    ffprobe_path: string;
	    bundled: boolean;
	    downloading: boolean;
	    supported: boolean;

	    static createFrom(source: any = {}) {
	        return new FFmpegStatus(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.available = source["available"];
	        this.ffmpeg_path = source["ffmpeg_path"];
	        this.ffprobe_path = source["ffprobe_path"];
	        this.bundled = source["bundled"];
	        this.downloading = source["downloading"];
	        this.supported = source["supported"];
	    }
	}
	export class FilterFacets {
	    tags: Record<string, number>;
	    ratings: Record<string, number>;
	    total: number;
	
	    static createFrom(source: any = {}) {
	        return new FilterFacets(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.tags = source["tags"];
	        this.ratings = source["ratings"];
	        this.total = source["total"];
	    }
	}
	export class MediaDetailBundle {
	    detail?: model.Media;
	    files: string[];
	    previews: string[];
	    trailer: string;
	
	    static createFrom(source: any = {}) {
	        return new MediaDetailBundle(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.detail = this.convertValues(source["detail"], model.Media);
	        this.files = source["files"];
	        this.previews = source["previews"];
	        this.trailer = source["trailer"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ScanTaskInfo {
	    task_id: string;
	    library_id: string;
	    library_name: string;
	    mode: string;
	    status: string;
	    failure_stage?: string;
	    error?: string;
	    retryable: boolean;
	    // Go type: time
	    started_at: any;
	    // Go type: time
	    finished_at?: any;
	
	    static createFrom(source: any = {}) {
	        return new ScanTaskInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.task_id = source["task_id"];
	        this.library_id = source["library_id"];
	        this.library_name = source["library_name"];
	        this.mode = source["mode"];
	        this.status = source["status"];
	        this.failure_stage = source["failure_stage"];
	        this.error = source["error"];
	        this.retryable = source["retryable"];
	        this.started_at = this.convertValues(source["started_at"], null);
	        this.finished_at = this.convertValues(source["finished_at"], null);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class StatsItem {
	    name: string;
	    count: number;
	    image: string;
	    filter_value: string;
	
	    static createFrom(source: any = {}) {
	        return new StatsItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.count = source["count"];
	        this.image = source["image"];
	        this.filter_value = source["filter_value"];
	    }
	}
	export class TagWithCount {
	    id: string;
	    name: string;
	    category: string;
	    count: number;
	
	    static createFrom(source: any = {}) {
	        return new TagWithCount(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.category = source["category"];
	        this.count = source["count"];
	    }
	}
	export class UserFilter {
	    scores: number[];
	    tag_groups: string[][];
	
	    static createFrom(source: any = {}) {
	        return new UserFilter(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.scores = source["scores"];
	        this.tag_groups = source["tag_groups"];
	    }
	}

}

export namespace model {
	
	export class Library {
	    id: string;
	    name: string;
	    path: string;
	    type: string;
	    // Go type: time
	    last_scan?: any;
	    folder_paths?: string[];
	    view_mode?: string;
	    title_field?: string;
	    subtitle_field?: string;
	    media_count?: number;
	    total_size?: number;
	    prefer_local_nfo: boolean;
	    min_file_size: number;
	    enable_file_filter: boolean;
	    metadata_lang: string;
	    allow_adult_content: boolean;
	    auto_download_sub: boolean;
	    metadata_mode: string;
	    enable_file_watch: boolean;
	    // Go type: time
	    created_at: any;
	    // Go type: time
	    updated_at: any;
	
	    static createFrom(source: any = {}) {
	        return new Library(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.path = source["path"];
	        this.type = source["type"];
	        this.last_scan = this.convertValues(source["last_scan"], null);
	        this.folder_paths = source["folder_paths"];
	        this.view_mode = source["view_mode"];
	        this.title_field = source["title_field"];
	        this.subtitle_field = source["subtitle_field"];
	        this.media_count = source["media_count"];
	        this.total_size = source["total_size"];
	        this.prefer_local_nfo = source["prefer_local_nfo"];
	        this.min_file_size = source["min_file_size"];
	        this.enable_file_filter = source["enable_file_filter"];
	        this.metadata_lang = source["metadata_lang"];
	        this.allow_adult_content = source["allow_adult_content"];
	        this.auto_download_sub = source["auto_download_sub"];
	        this.metadata_mode = source["metadata_mode"];
	        this.enable_file_watch = source["enable_file_watch"];
	        this.created_at = this.convertValues(source["created_at"], null);
	        this.updated_at = this.convertValues(source["updated_at"], null);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Tag {
	    id: string;
	    name: string;
	    color: string;
	    icon: string;
	    category: string;
	    sort_order: number;
	    usage_count: number;
	    created_by: string;
	    // Go type: time
	    created_at: any;
	    // Go type: time
	    updated_at: any;
	
	    static createFrom(source: any = {}) {
	        return new Tag(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.color = source["color"];
	        this.icon = source["icon"];
	        this.category = source["category"];
	        this.sort_order = source["sort_order"];
	        this.usage_count = source["usage_count"];
	        this.created_by = source["created_by"];
	        this.created_at = this.convertValues(source["created_at"], null);
	        this.updated_at = this.convertValues(source["updated_at"], null);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class MediaActor {
	    id: string;
	    name: string;
	
	    static createFrom(source: any = {}) {
	        return new MediaActor(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	    }
	}
	export class Series {
	    id: string;
	    library_id: string;
	    title: string;
	    orig_title: string;
	    year: number;
	    overview: string;
	    poster_path: string;
	    backdrop_path: string;
	    rating: number;
	    genres: string;
	    folder_path: string;
	    season_count: number;
	    episode_count: number;
	    tmdb_id: number;
	    douban_id: string;
	    bangumi_id: number;
	    country: string;
	    language: string;
	    studio: string;
	    // Go type: time
	    created_at: any;
	    // Go type: time
	    updated_at: any;
	    episodes?: Media[];
	
	    static createFrom(source: any = {}) {
	        return new Series(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.library_id = source["library_id"];
	        this.title = source["title"];
	        this.orig_title = source["orig_title"];
	        this.year = source["year"];
	        this.overview = source["overview"];
	        this.poster_path = source["poster_path"];
	        this.backdrop_path = source["backdrop_path"];
	        this.rating = source["rating"];
	        this.genres = source["genres"];
	        this.folder_path = source["folder_path"];
	        this.season_count = source["season_count"];
	        this.episode_count = source["episode_count"];
	        this.tmdb_id = source["tmdb_id"];
	        this.douban_id = source["douban_id"];
	        this.bangumi_id = source["bangumi_id"];
	        this.country = source["country"];
	        this.language = source["language"];
	        this.studio = source["studio"];
	        this.created_at = this.convertValues(source["created_at"], null);
	        this.updated_at = this.convertValues(source["updated_at"], null);
	        this.episodes = this.convertValues(source["episodes"], Media);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Media {
	    id: string;
	    library_id: string;
	    title: string;
	    orig_title: string;
	    year: number;
	    overview: string;
	    poster_path: string;
	    backdrop_path: string;
	    rating: number;
	    runtime: number;
	    genres: string;
	    file_path: string;
	    file_size: number;
	    // Go type: time
	    file_created_at?: any;
	    // Go type: time
	    file_mod_time?: any;
	    // Go type: time
	    nfo_mod_time?: any;
	    // Go type: time
	    library_added_at?: any;
	    video_fingerprint: string;
	    sidecar_fingerprint: string;
	    media_type: string;
	    video_codec: string;
	    audio_codec: string;
	    resolution: string;
	    duration: number;
	    subtitle_paths: string;
	    stream_url: string;
	    tmdb_id: number;
	    douban_id: string;
	    bangumi_id: number;
	    country: string;
	    language: string;
	    tagline: string;
	    studio: string;
	    maker: string;
	    label: string;
	    code: string;
	    code_prefix: string;
	    metadata_score: number;
	    trailer_url: string;
	    stack_group: string;
	    stack_order: number;
	    version_tag: string;
	    version_group: string;
	    nfo_extra_fields: string;
	    release_date_normalized: string;
	    metadata_phase: string;
	    scrape_status: string;
	    scrape_attempts: number;
	    // Go type: time
	    last_scrape_at?: any;
	    thumbnail_status: string;
	    thumbnail_retry_count: number;
	    // Go type: time
	    thumbnail_next_attempt_at?: any;
	    // Go type: time
	    thumbnail_locked_at?: any;
	    thumbnail_locked_by: string;
	    thumbnail_fingerprint: string;
	    thumbnail_error: string;
	    // Go type: time
	    thumbnail_updated_at?: any;
	    series_id: string;
	    season_num: number;
	    episode_num: number;
	    episode_title: string;
	    // Go type: time
	    created_at: any;
	    // Go type: time
	    updated_at: any;
	    series?: Series;
	    actor: string;
	    actors?: MediaActor[];
	    search_text: string;
	    is_favorite: boolean;
	    my_rating: number;
	    my_tags: Tag[];
	    is_watched: boolean;
	    position: number;
	    watch_duration: number;
	    progress_percent: number;
	    // Go type: time
	    last_watched_at?: any;
	
	    static createFrom(source: any = {}) {
	        return new Media(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.library_id = source["library_id"];
	        this.title = source["title"];
	        this.orig_title = source["orig_title"];
	        this.year = source["year"];
	        this.overview = source["overview"];
	        this.poster_path = source["poster_path"];
	        this.backdrop_path = source["backdrop_path"];
	        this.rating = source["rating"];
	        this.runtime = source["runtime"];
	        this.genres = source["genres"];
	        this.file_path = source["file_path"];
	        this.file_size = source["file_size"];
	        this.file_created_at = this.convertValues(source["file_created_at"], null);
	        this.file_mod_time = this.convertValues(source["file_mod_time"], null);
	        this.nfo_mod_time = this.convertValues(source["nfo_mod_time"], null);
	        this.library_added_at = this.convertValues(source["library_added_at"], null);
	        this.video_fingerprint = source["video_fingerprint"];
	        this.sidecar_fingerprint = source["sidecar_fingerprint"];
	        this.media_type = source["media_type"];
	        this.video_codec = source["video_codec"];
	        this.audio_codec = source["audio_codec"];
	        this.resolution = source["resolution"];
	        this.duration = source["duration"];
	        this.subtitle_paths = source["subtitle_paths"];
	        this.stream_url = source["stream_url"];
	        this.tmdb_id = source["tmdb_id"];
	        this.douban_id = source["douban_id"];
	        this.bangumi_id = source["bangumi_id"];
	        this.country = source["country"];
	        this.language = source["language"];
	        this.tagline = source["tagline"];
	        this.studio = source["studio"];
	        this.maker = source["maker"];
	        this.label = source["label"];
	        this.code = source["code"];
	        this.code_prefix = source["code_prefix"];
	        this.metadata_score = source["metadata_score"];
	        this.trailer_url = source["trailer_url"];
	        this.stack_group = source["stack_group"];
	        this.stack_order = source["stack_order"];
	        this.version_tag = source["version_tag"];
	        this.version_group = source["version_group"];
	        this.nfo_extra_fields = source["nfo_extra_fields"];
	        this.release_date_normalized = source["release_date_normalized"];
	        this.metadata_phase = source["metadata_phase"];
	        this.scrape_status = source["scrape_status"];
	        this.scrape_attempts = source["scrape_attempts"];
	        this.last_scrape_at = this.convertValues(source["last_scrape_at"], null);
	        this.thumbnail_status = source["thumbnail_status"];
	        this.thumbnail_retry_count = source["thumbnail_retry_count"];
	        this.thumbnail_next_attempt_at = this.convertValues(source["thumbnail_next_attempt_at"], null);
	        this.thumbnail_locked_at = this.convertValues(source["thumbnail_locked_at"], null);
	        this.thumbnail_locked_by = source["thumbnail_locked_by"];
	        this.thumbnail_fingerprint = source["thumbnail_fingerprint"];
	        this.thumbnail_error = source["thumbnail_error"];
	        this.thumbnail_updated_at = this.convertValues(source["thumbnail_updated_at"], null);
	        this.series_id = source["series_id"];
	        this.season_num = source["season_num"];
	        this.episode_num = source["episode_num"];
	        this.episode_title = source["episode_title"];
	        this.created_at = this.convertValues(source["created_at"], null);
	        this.updated_at = this.convertValues(source["updated_at"], null);
	        this.series = this.convertValues(source["series"], Series);
	        this.actor = source["actor"];
	        this.actors = this.convertValues(source["actors"], MediaActor);
	        this.search_text = source["search_text"];
	        this.is_favorite = source["is_favorite"];
	        this.my_rating = source["my_rating"];
	        this.my_tags = this.convertValues(source["my_tags"], Tag);
	        this.is_watched = source["is_watched"];
	        this.position = source["position"];
	        this.watch_duration = source["watch_duration"];
	        this.progress_percent = source["progress_percent"];
	        this.last_watched_at = this.convertValues(source["last_watched_at"], null);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	

}

export namespace repository {
	
	export class OrphanedMediaCleanupResult {
	    MediaPeople: number;
	    WatchHistories: number;
	    Favorites: number;
	    TranscodeTasks: number;
	    PlaylistItems: number;
	    Bookmarks: number;
	    Comments: number;
	    ContentRatings: number;
	    PlaybackStats: number;
	    VideoChapters: number;
	    VideoHighlights: number;
	    AIAnalysisTasks: number;
	    CoverCandidates: number;
	    MediaTags: number;
	    MediaRatings: number;
	    MediaShares: number;
	    MediaLikes: number;
	    Recommendations: number;
	    ShareLinks: number;
	    People: number;
	
	    static createFrom(source: any = {}) {
	        return new OrphanedMediaCleanupResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.MediaPeople = source["MediaPeople"];
	        this.WatchHistories = source["WatchHistories"];
	        this.Favorites = source["Favorites"];
	        this.TranscodeTasks = source["TranscodeTasks"];
	        this.PlaylistItems = source["PlaylistItems"];
	        this.Bookmarks = source["Bookmarks"];
	        this.Comments = source["Comments"];
	        this.ContentRatings = source["ContentRatings"];
	        this.PlaybackStats = source["PlaybackStats"];
	        this.VideoChapters = source["VideoChapters"];
	        this.VideoHighlights = source["VideoHighlights"];
	        this.AIAnalysisTasks = source["AIAnalysisTasks"];
	        this.CoverCandidates = source["CoverCandidates"];
	        this.MediaTags = source["MediaTags"];
	        this.MediaRatings = source["MediaRatings"];
	        this.MediaShares = source["MediaShares"];
	        this.MediaLikes = source["MediaLikes"];
	        this.Recommendations = source["Recommendations"];
	        this.ShareLinks = source["ShareLinks"];
	        this.People = source["People"];
	    }
	}

}

export namespace service {
	
	export class RelatedMediaItem {
	    media: model.Media;
	    reason: string;
	    match_type: string;
	    score: number;
	    matched_values?: string[];
	
	    static createFrom(source: any = {}) {
	        return new RelatedMediaItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.media = this.convertValues(source["media"], model.Media);
	        this.reason = source["reason"];
	        this.match_type = source["match_type"];
	        this.score = source["score"];
	        this.matched_values = source["matched_values"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class DetailRecommendationResponse {
	    continue_watching: RelatedMediaItem[];
	    more_like_this: RelatedMediaItem[];
	
	    static createFrom(source: any = {}) {
	        return new DetailRecommendationResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.continue_watching = this.convertValues(source["continue_watching"], RelatedMediaItem);
	        this.more_like_this = this.convertValues(source["more_like_this"], RelatedMediaItem);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class NFOEditorData {
	    nfo_path: string;
	    title: string;
	    code: string;
	    release_date: string;
	    director: string;
	    series: string;
	    publisher: string;
	    maker: string;
	    genres: string;
	    actors: string;
	    plot: string;
	    runtime: string;
	    file_size: string;
	    resolution: string;
	    video_codec: string;
	    rating: string;
	    source_fingerprint: string;
	    updated_fields: string[];
	
	    static createFrom(source: any = {}) {
	        return new NFOEditorData(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.nfo_path = source["nfo_path"];
	        this.title = source["title"];
	        this.code = source["code"];
	        this.release_date = source["release_date"];
	        this.director = source["director"];
	        this.series = source["series"];
	        this.publisher = source["publisher"];
	        this.maker = source["maker"];
	        this.genres = source["genres"];
	        this.actors = source["actors"];
	        this.plot = source["plot"];
	        this.runtime = source["runtime"];
	        this.file_size = source["file_size"];
	        this.resolution = source["resolution"];
	        this.video_codec = source["video_codec"];
	        this.rating = source["rating"];
	        this.source_fingerprint = source["source_fingerprint"];
	        this.updated_fields = source["updated_fields"];
	    }
	}
	
	export class ThumbnailTaskEventData {
	    task_id: string;
	    media_id: string;
	    library_id: string;
	    path: string;
	    type: string;
	    status: string;
	    phase: string;
	    message: string;
	    retryable: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ThumbnailTaskEventData(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.task_id = source["task_id"];
	        this.media_id = source["media_id"];
	        this.library_id = source["library_id"];
	        this.path = source["path"];
	        this.type = source["type"];
	        this.status = source["status"];
	        this.phase = source["phase"];
	        this.message = source["message"];
	        this.retryable = source["retryable"];
	    }
	}

}

