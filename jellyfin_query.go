package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"navi-desktop/model"
)

func (a *App) queryJellyfinResumeItems(r *http.Request) ([]*jellyfinResolvedItem, int, int, error) {
	startIndex := parsePositiveInt(r.URL.Query().Get("startIndex"))
	limit := parsePositiveInt(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > jellyfinMaxItems {
		limit = jellyfinMaxItems
	}
	baseSQL := `FROM watch_histories
		JOIN media ON media.id = watch_histories.media_id AND media.deleted_at IS NULL
		WHERE watch_histories.user_id = ? AND watch_histories.completed = 0`
	var total int64
	if err := a.db.Raw("SELECT COUNT(*) "+baseSQL, desktopUserID).Scan(&total).Error; err != nil {
		return nil, 0, 0, err
	}
	var refs []jellyfinItemRef
	if err := a.db.Raw("SELECT 'media' AS kind, media.id AS id "+baseSQL+" ORDER BY watch_histories.updated_at DESC, watch_histories.id ASC LIMIT ? OFFSET ?", desktopUserID, limit, startIndex).Scan(&refs).Error; err != nil {
		return nil, 0, 0, err
	}
	items, err := a.loadJellyfinItemRefs(refs)
	if err != nil {
		return nil, 0, 0, err
	}
	if err := a.hydrateJellyfinItems(items); err != nil {
		return nil, 0, 0, err
	}
	return items, int(total), startIndex, nil
}

func (a *App) queryJellyfinLatestItems(r *http.Request) ([]*jellyfinResolvedItem, error) {
	query := cloneValues(r.URL.Query())
	query.Set("recursive", "true")
	if strings.TrimSpace(query.Get("includeItemTypes")) == "" {
		query.Set("includeItemTypes", "Movie,Episode")
	}
	query.Set("sortBy", "DateCreated")
	query.Set("sortOrder", "Descending")
	request := r.Clone(r.Context())
	request.URL = cloneURL(r.URL)
	request.URL.RawQuery = query.Encode()
	items, _, _, err := a.queryJellyfinItemsSQL(request)
	return items, err
}

func cloneValues(values url.Values) url.Values {
	clone := make(url.Values, len(values))
	for key, entries := range values {
		clone[key] = append([]string(nil), entries...)
	}
	return clone
}

const (
	jellyfinMaxItems   = 500
	jellyfinQueryBatch = 400
)

type jellyfinItemRef struct {
	Kind string `gorm:"column:kind"`
	ID   string `gorm:"column:id"`
}

type jellyfinQuerySource struct {
	SQL  string
	Args []interface{}
}

func (a *App) queryJellyfinItemsSQL(r *http.Request) ([]*jellyfinResolvedItem, int, int, error) {
	query := r.URL.Query()
	if ids := splitCSV(query.Get("ids")); len(ids) > 0 {
		items, err := a.resolveJellyfinItems(ids)
		return items, len(items), 0, err
	}

	sources, err := a.jellyfinQuerySources(strings.TrimSpace(query.Get("parentId")), parseBool(query.Get("recursive")))
	if err != nil {
		return nil, 0, 0, err
	}
	if len(sources) == 0 {
		return []*jellyfinResolvedItem{}, 0, 0, nil
	}

	unionParts := make([]string, 0, len(sources))
	args := make([]interface{}, 0)
	for _, source := range sources {
		unionParts = append(unionParts, source.SQL)
		args = append(args, source.Args...)
	}
	baseSQL := "SELECT * FROM (" + strings.Join(unionParts, " UNION ALL ") + ") AS jellyfin_items"
	conditions := make([]string, 0, 4)
	filterArgs := make([]interface{}, 0)

	if includeTypes := splitCSV(query.Get("includeItemTypes")); len(includeTypes) > 0 {
		placeholders := make([]string, 0, len(includeTypes))
		for _, itemType := range includeTypes {
			placeholders = append(placeholders, "?")
			filterArgs = append(filterArgs, strings.ToLower(strings.TrimSpace(itemType)))
		}
		conditions = append(conditions, "LOWER(item_type) IN ("+strings.Join(placeholders, ",")+")")
	}
	if searchTerm := strings.TrimSpace(query.Get("searchTerm")); searchTerm != "" {
		conditions = append(conditions, "LOWER(name) LIKE LOWER(?)")
		filterArgs = append(filterArgs, "%"+searchTerm+"%")
	}
	if value := strings.TrimSpace(query.Get("isFavorite")); value != "" {
		comparison := "NOT EXISTS"
		if parseBool(value) {
			comparison = "EXISTS"
		}
		conditions = append(conditions, "kind = 'media' AND "+comparison+" (SELECT 1 FROM favorites WHERE favorites.user_id = ? AND favorites.media_id = jellyfin_items.id)")
		filterArgs = append(filterArgs, desktopUserID)
	}
	if value := strings.TrimSpace(query.Get("isPlayed")); value != "" {
		comparison := "NOT EXISTS"
		if parseBool(value) {
			comparison = "EXISTS"
		}
		conditions = append(conditions, "kind = 'media' AND "+comparison+" (SELECT 1 FROM watch_histories WHERE watch_histories.user_id = ? AND watch_histories.media_id = jellyfin_items.id AND watch_histories.completed = 1)")
		filterArgs = append(filterArgs, desktopUserID)
	}
	if len(conditions) > 0 {
		baseSQL += " WHERE " + strings.Join(conditions, " AND ")
	}

	allArgs := append(append([]interface{}{}, args...), filterArgs...)
	var total int64
	if err := a.db.Raw("SELECT COUNT(*) FROM ("+baseSQL+") AS jellyfin_count", allArgs...).Scan(&total).Error; err != nil {
		return nil, 0, 0, err
	}

	startIndex := parsePositiveInt(query.Get("startIndex"))
	limit := parsePositiveInt(query.Get("limit"))
	if limit <= 0 || limit > jellyfinMaxItems {
		limit = jellyfinMaxItems
	}
	orderDirection := "ASC"
	if orders := splitCSV(query.Get("sortOrder")); len(orders) > 0 && strings.EqualFold(strings.TrimSpace(orders[0]), "descending") {
		orderDirection = "DESC"
	}
	orderField := "name COLLATE NOCASE"
	if sorts := splitCSV(query.Get("sortBy")); len(sorts) > 0 {
		switch strings.ToLower(strings.TrimSpace(sorts[0])) {
		case "datecreated":
			orderField = "created_at"
		case "productionyear":
			orderField = "year"
		}
	}

	var refs []jellyfinItemRef
	pageSQL := baseSQL + " ORDER BY " + orderField + " " + orderDirection + ", id ASC LIMIT ? OFFSET ?"
	pageArgs := append(append([]interface{}{}, allArgs...), limit, startIndex)
	if err := a.db.Raw(pageSQL, pageArgs...).Scan(&refs).Error; err != nil {
		return nil, 0, 0, err
	}
	items, err := a.loadJellyfinItemRefs(refs)
	if err != nil {
		return nil, 0, 0, err
	}
	if err := a.hydrateJellyfinItems(items); err != nil {
		return nil, 0, 0, err
	}
	return items, int(total), startIndex, nil
}

func (a *App) jellyfinQuerySources(parentID string, recursive bool) ([]jellyfinQuerySource, error) {
	libraryTypeFilter := func(alias string, series bool) string {
		if series {
			return fmt.Sprintf("(%s.type = '' OR LOWER(%s.type) LIKE '%%tv%%' OR LOWER(%s.type) LIKE '%%series%%' OR LOWER(%s.type) LIKE '%%mixed%%')", alias, alias, alias, alias)
		}
		return fmt.Sprintf("(%s.type = '' OR LOWER(%s.type) LIKE '%%movie%%' OR LOWER(%s.type) LIKE '%%mixed%%' OR LOWER(%s.type) LIKE '%%other%%')", alias, alias, alias, alias)
	}
	librarySource := func(where string, args ...interface{}) jellyfinQuerySource {
		return jellyfinQuerySource{SQL: "SELECT 'library' AS kind, 'CollectionFolder' AS item_type, id, name, created_at, 0 AS year FROM libraries WHERE deleted_at IS NULL" + where, Args: args}
	}
	seriesSource := func(where string, args ...interface{}) jellyfinQuerySource {
		return jellyfinQuerySource{SQL: "SELECT 'series' AS kind, 'Series' AS item_type, series.id, series.title AS name, series.created_at, series.year FROM series JOIN libraries ON libraries.id = series.library_id AND libraries.deleted_at IS NULL WHERE series.deleted_at IS NULL AND series.episode_count > 0" + where, Args: args}
	}
	mediaSource := func(where string, args ...interface{}) jellyfinQuerySource {
		return jellyfinQuerySource{SQL: "SELECT 'media' AS kind, CASE WHEN media.series_id IS NOT NULL AND media.series_id <> '' THEN 'Episode' ELSE 'Movie' END AS item_type, media.id, CASE WHEN media.episode_title <> '' THEN media.episode_title ELSE media.title END AS name, media.created_at, media.year FROM media JOIN libraries ON libraries.id = media.library_id AND libraries.deleted_at IS NULL WHERE media.deleted_at IS NULL" + where, Args: args}
	}

	if parentID == "" {
		if !recursive {
			return []jellyfinQuerySource{librarySource("")}, nil
		}
		return []jellyfinQuerySource{
			seriesSource(" AND " + libraryTypeFilter("libraries", true)),
			mediaSource(" AND (((media.series_id IS NULL OR media.series_id = '') AND " + libraryTypeFilter("libraries", false) + ") OR ((media.series_id IS NOT NULL AND media.series_id <> '') AND " + libraryTypeFilter("libraries", true) + "))"),
		}, nil
	}

	parent, err := a.resolveJellyfinItem(parentID)
	if err != nil {
		return nil, err
	}
	switch parent.Kind {
	case "library":
		sources := make([]jellyfinQuerySource, 0, 3)
		if libraryAllowsSeries(parent.Library.Type) {
			sources = append(sources, seriesSource(" AND series.library_id = ?", parent.Library.ID))
			if recursive {
				sources = append(sources, mediaSource(" AND media.library_id = ? AND media.series_id IS NOT NULL AND media.series_id <> ''", parent.Library.ID))
			}
		}
		if libraryAllowsMovies(parent.Library.Type) {
			sources = append(sources, mediaSource(" AND media.library_id = ? AND (media.series_id IS NULL OR media.series_id = '')", parent.Library.ID))
		}
		return sources, nil
	case "series":
		return []jellyfinQuerySource{mediaSource(" AND media.series_id = ?", parent.Series.ID)}, nil
	default:
		return nil, nil
	}
}

func (a *App) resolveJellyfinItems(ids []string) ([]*jellyfinResolvedItem, error) {
	refs := make([]jellyfinItemRef, 0, len(ids))
	unprefixed := make([]string, 0)
	for _, rawID := range ids {
		id := strings.TrimSpace(rawID)
		switch {
		case strings.HasPrefix(id, "library:"):
			refs = append(refs, jellyfinItemRef{Kind: "library", ID: strings.TrimPrefix(id, "library:")})
		case strings.HasPrefix(id, "series:"):
			refs = append(refs, jellyfinItemRef{Kind: "series", ID: strings.TrimPrefix(id, "series:")})
		case strings.HasPrefix(id, "media:"):
			refs = append(refs, jellyfinItemRef{Kind: "media", ID: strings.TrimPrefix(id, "media:")})
		default:
			refs = append(refs, jellyfinItemRef{ID: id})
			unprefixed = append(unprefixed, id)
		}
	}
	resolvedKinds := make(map[string]string)
	for _, target := range []struct{ kind, table string }{{"library", "libraries"}, {"series", "series"}, {"media", "media"}} {
		for start := 0; start < len(unprefixed); start += jellyfinQueryBatch {
			end := minInt(start+jellyfinQueryBatch, len(unprefixed))
			var found []string
			if err := a.db.Table(target.table).Where("deleted_at IS NULL AND id IN ?", unprefixed[start:end]).Pluck("id", &found).Error; err != nil {
				return nil, err
			}
			for _, id := range found {
				if resolvedKinds[id] == "" {
					resolvedKinds[id] = target.kind
				}
			}
		}
	}
	resolvedRefs := refs[:0]
	for _, ref := range refs {
		if ref.Kind == "" {
			ref.Kind = resolvedKinds[ref.ID]
		}
		if ref.Kind != "" {
			resolvedRefs = append(resolvedRefs, ref)
		}
	}
	refs = resolvedRefs
	items, err := a.loadJellyfinItemRefs(refs)
	if err != nil {
		return nil, err
	}
	if err := a.hydrateJellyfinItems(items); err != nil {
		return nil, err
	}
	return items, nil
}

func (a *App) loadJellyfinItemRefs(refs []jellyfinItemRef) ([]*jellyfinResolvedItem, error) {
	byKind := map[string][]string{"library": {}, "series": {}, "media": {}}
	for _, ref := range refs {
		byKind[ref.Kind] = append(byKind[ref.Kind], ref.ID)
	}
	libraries := make(map[string]*model.Library)
	seriesByID := make(map[string]*model.Series)
	mediaByID := make(map[string]*model.Media)
	for kind, ids := range byKind {
		for start := 0; start < len(ids); start += jellyfinQueryBatch {
			end := start + jellyfinQueryBatch
			if end > len(ids) {
				end = len(ids)
			}
			switch kind {
			case "library":
				var rows []model.Library
				if err := a.db.Select("id, name, type, created_at").Where("id IN ?", ids[start:end]).Find(&rows).Error; err != nil {
					return nil, err
				}
				for i := range rows {
					rows[i].HydratePathConfig()
					libraries[rows[i].ID] = &rows[i]
				}
			case "series":
				var rows []model.Series
				if err := a.db.Select("id, library_id, title, orig_title, year, overview, poster_path, backdrop_path, genres, season_count, episode_count, created_at, updated_at").Where("id IN ?", ids[start:end]).Find(&rows).Error; err != nil {
					return nil, err
				}
				for i := range rows {
					seriesByID[rows[i].ID] = &rows[i]
				}
			case "media":
				var rows []model.Media
				if err := a.db.Select("id, library_id, title, orig_title, year, overview, poster_path, backdrop_path, rating, runtime, genres, file_path, file_size, media_type, video_codec, audio_codec, resolution, duration, subtitle_paths, stream_url, series_id, season_num, episode_num, episode_title, created_at, updated_at").Where("id IN ?", ids[start:end]).Find(&rows).Error; err != nil {
					return nil, err
				}
				for i := range rows {
					mediaByID[rows[i].ID] = &rows[i]
				}
			}
		}
	}

	items := make([]*jellyfinResolvedItem, 0, len(refs))
	for _, ref := range refs {
		item := &jellyfinResolvedItem{ID: ref.Kind + ":" + ref.ID, Kind: ref.Kind}
		switch ref.Kind {
		case "library":
			item.Library = libraries[ref.ID]
			if item.Library == nil {
				continue
			}
		case "series":
			item.Series = seriesByID[ref.ID]
			if item.Series == nil {
				continue
			}
		case "media":
			item.Media = mediaByID[ref.ID]
			if item.Media == nil {
				continue
			}
		default:
			continue
		}
		items = append(items, item)
	}
	return items, nil
}

func (a *App) hydrateJellyfinItems(items []*jellyfinResolvedItem) error {
	mediaItems := make(map[string][]*jellyfinResolvedItem)
	seriesItems := make(map[string][]*jellyfinResolvedItem)
	libraryItems := make(map[string][]*jellyfinResolvedItem)
	for _, item := range items {
		switch {
		case item != nil && item.Media != nil:
			mediaItems[item.Media.ID] = append(mediaItems[item.Media.ID], item)
		case item != nil && item.Series != nil:
			seriesItems[item.Series.ID] = append(seriesItems[item.Series.ID], item)
		case item != nil && item.Library != nil:
			libraryItems[item.Library.ID] = append(libraryItems[item.Library.ID], item)
		}
	}

	mediaIDs := mapKeys(mediaItems)
	for start := 0; start < len(mediaIDs); start += jellyfinQueryBatch {
		end := minInt(start+jellyfinQueryBatch, len(mediaIDs))
		type stateRow struct {
			MediaID     string     `gorm:"column:media_id"`
			SeriesName  string     `gorm:"column:series_name"`
			FavoriteID  *string    `gorm:"column:favorite_id"`
			HistoryID   *string    `gorm:"column:history_id"`
			Position    float64    `gorm:"column:position"`
			Duration    float64    `gorm:"column:duration"`
			Completed   bool       `gorm:"column:completed"`
			HistoryTime *time.Time `gorm:"column:history_time"`
		}
		var rows []stateRow
		if err := a.db.Raw(`SELECT media.id AS media_id, COALESCE(series.title, '') AS series_name,
			favorites.id AS favorite_id, watch_histories.id AS history_id,
			COALESCE(watch_histories.position, 0) AS position,
			COALESCE(watch_histories.duration, 0) AS duration,
			COALESCE(watch_histories.completed, 0) AS completed,
			watch_histories.updated_at AS history_time
			FROM media
			LEFT JOIN series ON series.id = media.series_id AND series.deleted_at IS NULL
			LEFT JOIN favorites ON favorites.media_id = media.id AND favorites.user_id = ?
			LEFT JOIN watch_histories ON watch_histories.media_id = media.id AND watch_histories.user_id = ?
			WHERE media.deleted_at IS NULL AND media.id IN ?`, desktopUserID, desktopUserID, mediaIDs[start:end]).Scan(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			for _, item := range mediaItems[row.MediaID] {
				if row.SeriesName != "" {
					item.Media.Series = &model.Series{Title: row.SeriesName}
				}
				item.UserData = jellyfinUserDataFromState(row.MediaID, row.FavoriteID != nil, row.HistoryID != nil, row.Position, row.Duration, row.Completed, row.HistoryTime)
			}
		}
	}

	seriesIDs := mapKeys(seriesItems)
	for _, matchingItems := range seriesItems {
		for _, item := range matchingItems {
			item.CountsLoaded = true
			item.Series.EpisodeCount = 0
			item.Series.SeasonCount = 0
		}
	}
	for start := 0; start < len(seriesIDs); start += jellyfinQueryBatch {
		end := minInt(start+jellyfinQueryBatch, len(seriesIDs))
		type countRow struct {
			SeriesID     string `gorm:"column:series_id"`
			EpisodeCount int    `gorm:"column:episode_count"`
			SeasonCount  int    `gorm:"column:season_count"`
		}
		var rows []countRow
		if err := a.db.Model(&model.Media{}).
			Select("series_id, COUNT(*) AS episode_count, COUNT(DISTINCT season_num) AS season_count").
			Where("series_id IN ?", seriesIDs[start:end]).Group("series_id").Scan(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			for _, item := range seriesItems[row.SeriesID] {
				item.Series.EpisodeCount = row.EpisodeCount
				item.Series.SeasonCount = row.SeasonCount
			}
		}
	}

	if len(libraryItems) > 0 {
		libraryIDs := mapKeys(libraryItems)
		seriesCounts := make(map[string]int)
		mediaCounts := make(map[string]int)
		type libraryCountRow struct {
			LibraryID string `gorm:"column:library_id"`
			Count     int    `gorm:"column:count"`
		}
		for start := 0; start < len(libraryIDs); start += jellyfinQueryBatch {
			end := minInt(start+jellyfinQueryBatch, len(libraryIDs))
			var seriesRows []libraryCountRow
			if err := a.db.Model(&model.Series{}).Select("library_id, COUNT(*) AS count").Where("library_id IN ? AND episode_count > 0", libraryIDs[start:end]).Group("library_id").Scan(&seriesRows).Error; err != nil {
				return err
			}
			for _, row := range seriesRows {
				seriesCounts[row.LibraryID] = row.Count
			}
			var mediaRows []libraryCountRow
			if err := a.db.Model(&model.Media{}).Select("library_id, COUNT(*) AS count").Where("library_id IN ? AND (series_id IS NULL OR series_id = '')", libraryIDs[start:end]).Group("library_id").Scan(&mediaRows).Error; err != nil {
				return err
			}
			for _, row := range mediaRows {
				mediaCounts[row.LibraryID] = row.Count
			}
		}
		for id, matchingItems := range libraryItems {
			for _, item := range matchingItems {
				item.CountsLoaded = true
				item.Library.MediaCount = 0
				if libraryAllowsSeries(item.Library.Type) {
					item.Library.MediaCount += seriesCounts[id]
				}
				if libraryAllowsMovies(item.Library.Type) {
					item.Library.MediaCount += mediaCounts[id]
				}
			}
		}
	}
	return nil
}

func mapKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func jellyfinUserDataFromState(mediaID string, favorite bool, hasHistory bool, position, duration float64, completed bool, updatedAt *time.Time) map[string]any {
	result := map[string]any{
		"ItemId": "media:" + mediaID, "Key": mediaID, "IsFavorite": favorite,
		"Played": completed, "PlaybackPositionTicks": int64(position * float64(jellyfinTicksPerSecond)), "PlayCount": 0,
	}
	if completed {
		result["PlayCount"] = 1
	}
	if hasHistory && duration > 0 && position > 0 {
		result["PlayedPercentage"] = minFloat64(100, position/duration*100)
	}
	if hasHistory && updatedAt != nil && !updatedAt.IsZero() {
		result["LastPlayedDate"] = updatedAt.UTC().Format(time.RFC3339)
	}
	return result
}
