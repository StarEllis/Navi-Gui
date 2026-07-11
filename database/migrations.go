package database

import (
	"fmt"

	"gorm.io/gorm"
	"navi-desktop/model"
)

func DefaultMigrations() []Migration {
	return []Migration{
		{
			Version: 1,
			Name:    "baseline_schema",
			Apply: func(tx *gorm.DB) (MigrationStats, error) {
				stats, err := cleanKnownOrphans(tx)
				if err != nil {
					return nil, err
				}
				if count, err := deduplicateFavorites(tx); err != nil {
					return nil, err
				} else {
					stats["favorites_merged"] += count
				}
				if count, err := deduplicateWatchHistories(tx); err != nil {
					return nil, err
				} else {
					stats["watch_histories_merged"] += count
				}
				if err := model.AutoMigrate(tx); err != nil {
					return nil, fmt.Errorf("safe baseline AutoMigrate: %w", err)
				}
				return stats, nil
			},
		},
		{
			Version: 2,
			Name:    "normalized_path_identity_and_uniqueness",
			Apply: func(tx *gorm.DB) (MigrationStats, error) {
				return migratePathIdentity(tx)
			},
		},
		{
			Version: 3,
			Name:    "relational_integrity_cleanup",
			Apply: func(tx *gorm.DB) (MigrationStats, error) {
				stats, err := cleanKnownOrphans(tx)
				if err != nil {
					return nil, err
				}
				if err := ensureRequiredIndexes(tx); err != nil {
					return nil, err
				}
				return stats, nil
			},
		},
		{
			Version: 4,
			Name:    "jellyfin_query_indexes",
			Apply: func(tx *gorm.DB) (MigrationStats, error) {
				if err := ensureRequiredIndexes(tx); err != nil {
					return nil, err
				}
				return MigrationStats{}, nil
			},
		},
	}
}

type orphanRule struct {
	child    string
	column   string
	parent   string
	required bool
}

var orphanRules = []orphanRule{
	// Association and leaf rows come first. Parent rows whose library is gone
	// are deleted only after every reference to those rows has been handled.
	{"media_people", "media_id", "media", true},
	{"media_people", "series_id", "series", false},
	{"media_people", "person_id", "people", true},
	{"watch_histories", "user_id", "users", true},
	{"watch_histories", "media_id", "media", true},
	{"favorites", "user_id", "users", true},
	{"favorites", "media_id", "media", true},
	{"transcode_tasks", "media_id", "media", true},
	{"playlists", "user_id", "users", true},
	{"playlist_items", "playlist_id", "playlists", true},
	{"playlist_items", "media_id", "media", true},
	{"bookmarks", "user_id", "users", true},
	{"bookmarks", "media_id", "media", true},
	{"comments", "user_id", "users", true},
	{"comments", "media_id", "media", true},
	{"access_logs", "user_id", "users", true},
	{"content_ratings", "media_id", "media", true},
	{"user_permissions", "user_id", "users", true},
	{"playback_stats", "user_id", "users", true},
	{"playback_stats", "media_id", "media", true},
	{"scrape_tasks", "media_id", "media", false},
	{"scrape_tasks", "series_id", "series", false},
	{"scrape_histories", "task_id", "scrape_tasks", true},
	{"video_chapters", "media_id", "media", true},
	{"video_highlights", "media_id", "media", true},
	{"ai_analysis_tasks", "media_id", "media", true},
	{"cover_candidates", "media_id", "media", true},
	{"family_groups", "owner_id", "users", true},
	{"family_members", "group_id", "family_groups", true},
	{"family_members", "user_id", "users", true},
	{"media_shares", "user_id", "users", true},
	{"media_shares", "group_id", "family_groups", true},
	{"media_shares", "media_id", "media", false},
	{"media_shares", "series_id", "series", false},
	{"media_likes", "user_id", "users", true},
	{"media_likes", "media_id", "media", false},
	{"media_likes", "series_id", "series", false},
	{"media_likes", "comment_id", "comments", false},
	{"media_recommendations", "from_user_id", "users", true},
	{"media_recommendations", "to_user_id", "users", true},
	{"media_recommendations", "media_id", "media", false},
	{"media_recommendations", "series_id", "series", false},
	{"live_recordings", "source_id", "live_sources", true},
	{"live_recordings", "user_id", "users", true},
	{"sync_devices", "user_id", "users", true},
	{"sync_records", "user_id", "users", true},
	{"user_sync_configs", "user_id", "users", true},
	{"media_tags", "media_id", "media", true},
	{"media_tags", "tag_id", "tags", true},
	{"share_links", "created_by", "users", true},
	{"share_links", "media_id", "media", false},
	{"share_links", "series_id", "series", false},
	{"match_rules", "library_id", "libraries", false},
	{"media", "series_id", "series", false},
	{"media", "library_id", "libraries", true},
	{"series", "library_id", "libraries", true},
}

func cleanKnownOrphans(tx *gorm.DB) (MigrationStats, error) {
	stats := MigrationStats{}
	// A Series or Media row with no live Library is itself scheduled for
	// deletion. Remove/null references to those rows before deleting the parent,
	// even though the parent still exists at this point.
	liveLibraries := "SELECT id FROM libraries"
	if tx.Migrator().HasColumn("libraries", "deleted_at") {
		liveLibraries += " WHERE deleted_at IS NULL"
	}
	for _, parent := range []string{"media", "series"} {
		if !hasTableAndColumn(tx, parent, "library_id") || !tx.Migrator().HasTable("libraries") {
			continue
		}
		if parent == "media" && hasTableAndColumn(tx, "comments", "media_id") && hasTableAndColumn(tx, "media_likes", "comment_id") {
			result := tx.Exec(fmt.Sprintf(`UPDATE media_likes SET comment_id = NULL
				WHERE comment_id IN (SELECT id FROM comments WHERE media_id IN (
					SELECT id FROM media WHERE library_id NOT IN (%s)
				))`, liveLibraries))
			if result.Error != nil {
				return nil, fmt.Errorf("clean likes for comments on doomed media: %w", result.Error)
			}
			stats["doomed_media_references_media_likes_comment_id"] += result.RowsAffected
		}
		for _, rule := range orphanRules {
			if rule.parent != parent || !hasTableAndColumn(tx, rule.child, rule.column) {
				continue
			}
			condition := fmt.Sprintf("%s IN (SELECT id FROM %s WHERE library_id NOT IN (%s))", rule.column, parent, liveLibraries)
			var result *gorm.DB
			if rule.required {
				result = tx.Exec(fmt.Sprintf("DELETE FROM %s WHERE %s", rule.child, condition))
			} else {
				result = tx.Exec(fmt.Sprintf("UPDATE %s SET %s = NULL WHERE %s", rule.child, rule.column, condition))
			}
			if result.Error != nil {
				return nil, fmt.Errorf("clean reference to doomed %s from %s.%s: %w", parent, rule.child, rule.column, result.Error)
			}
			stats["doomed_"+parent+"_references_"+rule.child+"_"+rule.column] += result.RowsAffected
		}
	}
	for _, rule := range orphanRules {
		if !hasTableAndColumn(tx, rule.child, rule.column) || !tx.Migrator().HasTable(rule.parent) {
			continue
		}
		parentFilter := ""
		if tx.Migrator().HasColumn(rule.parent, "deleted_at") {
			parentFilter = " WHERE deleted_at IS NULL"
		}
		missing := fmt.Sprintf(
			"%s IS NOT NULL AND %s <> '' AND %s NOT IN (SELECT id FROM %s%s)",
			rule.column, rule.column, rule.column, rule.parent, parentFilter,
		)
		var result *gorm.DB
		if rule.required {
			result = tx.Exec(fmt.Sprintf("DELETE FROM %s WHERE %s", rule.child, missing))
		} else {
			normalized := tx.Exec(fmt.Sprintf(
				"UPDATE %s SET %s = NULL WHERE %s = ''",
				rule.child, rule.column, rule.column,
			))
			if normalized.Error != nil {
				return nil, fmt.Errorf("normalize optional relation %s.%s: %w", rule.child, rule.column, normalized.Error)
			}
			stats["optional_"+rule.child+"_"+rule.column+"_normalized"] += normalized.RowsAffected
			result = tx.Exec(fmt.Sprintf("UPDATE %s SET %s = NULL WHERE %s", rule.child, rule.column, missing))
		}
		if result.Error != nil {
			return nil, fmt.Errorf("clean orphan relation %s.%s: %w", rule.child, rule.column, result.Error)
		}
		stats["orphan_"+rule.child+"_"+rule.column] += result.RowsAffected
	}
	if tx.Migrator().HasTable("people") && tx.Migrator().HasTable("media_people") {
		referenceCondition := "person_id <> '' AND media_id <> ''"
		if tx.Migrator().HasColumn("media_people", "series_id") {
			referenceCondition = "person_id <> '' AND (media_id <> '' OR series_id <> '')"
		}
		result := tx.Exec(fmt.Sprintf(`DELETE FROM people WHERE id NOT IN (
			SELECT DISTINCT person_id FROM media_people WHERE %s
		)`, referenceCondition))
		if result.Error != nil {
			return nil, fmt.Errorf("clean unreferenced people: %w", result.Error)
		}
		stats["orphan_people"] += result.RowsAffected
	}
	return stats, nil
}

func hasTableAndColumn(tx *gorm.DB, table, column string) bool {
	return tx.Migrator().HasTable(table) && tx.Migrator().HasColumn(table, column)
}

func deduplicateFavorites(tx *gorm.DB) (int64, error) {
	if !tx.Migrator().HasTable("favorites") {
		return 0, nil
	}
	result := tx.Exec(`
		DELETE FROM favorites
		WHERE id IN (
			SELECT id FROM (
				SELECT id, ROW_NUMBER() OVER (
					PARTITION BY user_id, media_id
					ORDER BY created_at ASC, id ASC
				) AS duplicate_number
				FROM favorites
			) WHERE duplicate_number > 1
		)`)
	return result.RowsAffected, result.Error
}

func deduplicateWatchHistories(tx *gorm.DB) (int64, error) {
	if !tx.Migrator().HasTable("watch_histories") {
		return 0, nil
	}
	result := tx.Exec(`
		DELETE FROM watch_histories
		WHERE id IN (
			SELECT id FROM (
				SELECT id, ROW_NUMBER() OVER (
					PARTITION BY user_id, media_id
					ORDER BY updated_at DESC, created_at DESC, id ASC
				) AS duplicate_number
				FROM watch_histories
			) WHERE duplicate_number > 1
		)`)
	return result.RowsAffected, result.Error
}

func migratePathIdentity(tx *gorm.DB) (MigrationStats, error) {
	stats := MigrationStats{}
	if err := tx.Exec("DROP INDEX IF EXISTS idx_series_folder_path").Error; err != nil {
		return nil, fmt.Errorf("drop legacy series folder index: %w", err)
	}

	var libraries []model.Library
	if err := tx.Unscoped().Find(&libraries).Error; err != nil {
		return nil, fmt.Errorf("load libraries for path migration: %w", err)
	}
	for i := range libraries {
		key := model.LibraryPathKey(libraries[i].Path)
		if libraries[i].PathKey == key {
			continue
		}
		if err := tx.Unscoped().Model(&model.Library{}).Where("id = ?", libraries[i].ID).UpdateColumn("path_key", key).Error; err != nil {
			return nil, fmt.Errorf("normalize library path %s: %w", libraries[i].ID, err)
		}
		stats["library_paths_normalized"]++
	}

	var media []model.Media
	if err := tx.Unscoped().Find(&media).Error; err != nil {
		return nil, fmt.Errorf("load media for path migration: %w", err)
	}
	for i := range media {
		key := model.NormalizePathKey(media[i].FilePath)
		if media[i].PathKey == key {
			continue
		}
		if err := tx.Unscoped().Model(&model.Media{}).Where("id = ?", media[i].ID).UpdateColumn("path_key", key).Error; err != nil {
			return nil, fmt.Errorf("normalize media path %s: %w", media[i].ID, err)
		}
		stats["media_paths_normalized"]++
	}

	var series []model.Series
	if err := tx.Unscoped().Find(&series).Error; err != nil {
		return nil, fmt.Errorf("load series for path migration: %w", err)
	}
	for i := range series {
		key := model.NormalizePathKey(series[i].FolderPath)
		if series[i].FolderPathKey == key {
			continue
		}
		if err := tx.Unscoped().Model(&model.Series{}).Where("id = ?", series[i].ID).UpdateColumn("folder_path_key", key).Error; err != nil {
			return nil, fmt.Errorf("normalize series path %s: %w", series[i].ID, err)
		}
		stats["series_paths_normalized"]++
	}

	mergedLibraries, err := mergeDuplicateLibraries(tx)
	if err != nil {
		return nil, err
	}
	stats["libraries_merged"] = mergedLibraries
	mergedMedia, err := mergeDuplicateMedia(tx)
	if err != nil {
		return nil, err
	}
	stats["media_merged"] = mergedMedia
	mergedSeries, err := mergeDuplicateSeries(tx)
	if err != nil {
		return nil, err
	}
	stats["series_merged"] = mergedSeries
	if count, err := deduplicateFavorites(tx); err != nil {
		return nil, err
	} else {
		stats["favorites_merged"] += count
	}
	if count, err := deduplicateWatchHistories(tx); err != nil {
		return nil, err
	} else {
		stats["watch_histories_merged"] += count
	}
	if err := ensureRequiredIndexes(tx); err != nil {
		return nil, err
	}
	return stats, nil
}

func mergeDuplicateLibraries(tx *gorm.DB) (int64, error) {
	type group struct {
		PathKey string
	}
	var groups []group
	if err := tx.Raw(`
		SELECT path_key FROM libraries
		WHERE deleted_at IS NULL AND path_key <> ''
		GROUP BY path_key HAVING COUNT(*) > 1`).Scan(&groups).Error; err != nil {
		return 0, err
	}
	var merged int64
	for _, group := range groups {
		var rows []model.Library
		if err := tx.Where("path_key = ?", group.PathKey).Order("created_at ASC, id ASC").Find(&rows).Error; err != nil {
			return merged, err
		}
		if len(rows) < 2 {
			continue
		}
		survivor := rows[0].ID
		for _, loser := range rows[1:] {
			for _, relation := range []struct{ table, column string }{
				{"media", "library_id"},
				{"series", "library_id"},
				{"match_rules", "library_id"},
			} {
				if hasTableAndColumn(tx, relation.table, relation.column) {
					if err := tx.Exec(fmt.Sprintf("UPDATE %s SET %s = ? WHERE %s = ?", relation.table, relation.column, relation.column), survivor, loser.ID).Error; err != nil {
						return merged, err
					}
				}
			}
			if err := tx.Unscoped().Delete(&model.Library{}, "id = ?", loser.ID).Error; err != nil {
				return merged, err
			}
			merged++
		}
	}
	return merged, nil
}

func mergeDuplicateMedia(tx *gorm.DB) (int64, error) {
	type group struct {
		LibraryID string
		PathKey   string
	}
	var groups []group
	if err := tx.Raw(`
		SELECT library_id, path_key FROM media
		WHERE deleted_at IS NULL AND path_key <> ''
		GROUP BY library_id, path_key HAVING COUNT(*) > 1`).Scan(&groups).Error; err != nil {
		return 0, err
	}
	var merged int64
	for _, group := range groups {
		var rows []model.Media
		if err := tx.Where("library_id = ? AND path_key = ?", group.LibraryID, group.PathKey).
			Order("updated_at DESC, created_at DESC, id ASC").Find(&rows).Error; err != nil {
			return merged, err
		}
		if len(rows) < 2 {
			continue
		}
		survivor := rows[0].ID
		for _, loser := range rows[1:] {
			if err := mergeMediaReferences(tx, survivor, loser.ID); err != nil {
				return merged, err
			}
			if err := tx.Unscoped().Delete(&model.Media{}, "id = ?", loser.ID).Error; err != nil {
				return merged, err
			}
			merged++
		}
	}
	return merged, nil
}

func mergeMediaReferences(tx *gorm.DB, survivor, loser string) error {
	if tx.Migrator().HasTable("favorites") {
		if err := tx.Exec(`DELETE FROM favorites WHERE media_id = ? AND user_id IN (SELECT user_id FROM favorites WHERE media_id = ?)`, loser, survivor).Error; err != nil {
			return err
		}
		if err := tx.Exec("UPDATE favorites SET media_id = ? WHERE media_id = ?", survivor, loser).Error; err != nil {
			return err
		}
	}
	if tx.Migrator().HasTable("watch_histories") {
		var rows []model.WatchHistory
		if err := tx.Where("media_id IN ?", []string{survivor, loser}).
			Order("updated_at DESC, created_at DESC, id ASC").Find(&rows).Error; err != nil {
			return err
		}
		seen := map[string]string{}
		for _, row := range rows {
			if _, exists := seen[row.UserID]; exists {
				if err := tx.Unscoped().Delete(&model.WatchHistory{}, "id = ?", row.ID).Error; err != nil {
					return err
				}
			} else {
				seen[row.UserID] = row.ID
				if row.MediaID != survivor {
					if err := tx.Model(&model.WatchHistory{}).Where("id = ?", row.ID).UpdateColumn("media_id", survivor).Error; err != nil {
						return err
					}
				}
			}
		}
	}
	if tx.Migrator().HasTable("media_tags") {
		if err := tx.Exec(`DELETE FROM media_tags WHERE media_id = ? AND tag_id IN (SELECT tag_id FROM media_tags WHERE media_id = ?)`, loser, survivor).Error; err != nil {
			return err
		}
		if err := tx.Exec("UPDATE media_tags SET media_id = ? WHERE media_id = ?", survivor, loser).Error; err != nil {
			return err
		}
	}
	if tx.Migrator().HasTable("content_ratings") {
		var count int64
		if err := tx.Table("content_ratings").Where("media_id = ?", survivor).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			if err := tx.Exec("DELETE FROM content_ratings WHERE media_id = ?", loser).Error; err != nil {
				return err
			}
		} else if err := tx.Exec("UPDATE content_ratings SET media_id = ? WHERE media_id = ?", survivor, loser).Error; err != nil {
			return err
		}
	}
	for _, table := range []string{
		"media_people", "transcode_tasks", "playlist_items", "bookmarks", "comments",
		"playback_stats", "scrape_tasks", "video_chapters", "video_highlights",
		"ai_analysis_tasks", "cover_candidates", "media_shares", "media_likes",
		"media_recommendations", "share_links",
	} {
		if hasTableAndColumn(tx, table, "media_id") {
			if err := tx.Exec(fmt.Sprintf("UPDATE %s SET media_id = ? WHERE media_id = ?", table), survivor, loser).Error; err != nil {
				return fmt.Errorf("merge %s media reference: %w", table, err)
			}
		}
	}
	return nil
}

func mergeDuplicateSeries(tx *gorm.DB) (int64, error) {
	type group struct {
		LibraryID     string
		FolderPathKey string
	}
	var groups []group
	if err := tx.Raw(`
		SELECT library_id, folder_path_key FROM series
		WHERE deleted_at IS NULL AND folder_path_key <> ''
		GROUP BY library_id, folder_path_key HAVING COUNT(*) > 1`).Scan(&groups).Error; err != nil {
		return 0, err
	}
	var merged int64
	for _, group := range groups {
		var rows []model.Series
		if err := tx.Where("library_id = ? AND folder_path_key = ?", group.LibraryID, group.FolderPathKey).
			Order("updated_at DESC, created_at DESC, id ASC").Find(&rows).Error; err != nil {
			return merged, err
		}
		if len(rows) < 2 {
			continue
		}
		survivor := rows[0].ID
		for _, loser := range rows[1:] {
			for _, table := range []string{
				"media", "media_people", "scrape_tasks", "media_shares",
				"media_likes", "media_recommendations", "share_links",
			} {
				if hasTableAndColumn(tx, table, "series_id") {
					if err := tx.Exec(fmt.Sprintf("UPDATE %s SET series_id = ? WHERE series_id = ?", table), survivor, loser.ID).Error; err != nil {
						return merged, fmt.Errorf("merge %s series reference: %w", table, err)
					}
				}
			}
			if err := tx.Unscoped().Delete(&model.Series{}, "id = ?", loser.ID).Error; err != nil {
				return merged, err
			}
			merged++
		}
	}
	return merged, nil
}
