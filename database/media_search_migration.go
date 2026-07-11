package database

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
	"navi-desktop/model"
)

const mediaSearchMigrationBatchSize = 200

type mediaSearchActorRow struct {
	MediaID  string
	Name     string
	OrigName string
}

func migrateMediaSearchFields(tx *gorm.DB) (MigrationStats, error) {
	if err := tx.AutoMigrate(&model.Media{}); err != nil {
		return nil, fmt.Errorf("add media search fields: %w", err)
	}

	stats := MigrationStats{}
	var lastID string
	for {
		var mediaItems []model.Media
		query := tx.Unscoped().Order("id ASC").Limit(mediaSearchMigrationBatchSize)
		if lastID != "" {
			query = query.Where("id > ?", lastID)
		}
		if err := query.Find(&mediaItems).Error; err != nil {
			return nil, fmt.Errorf("load media search migration batch: %w", err)
		}
		if len(mediaItems) == 0 {
			break
		}

		ids := make([]string, 0, len(mediaItems))
		for i := range mediaItems {
			ids = append(ids, mediaItems[i].ID)
		}
		var actorRows []mediaSearchActorRow
		if err := tx.Table("media_people").
			Select("media_people.media_id, people.name, people.orig_name").
			Joins("JOIN people ON people.id = media_people.person_id").
			Where("media_people.role = ? AND media_people.media_id IN ?", "actor", ids).
			Order("media_people.media_id ASC, media_people.sort_order ASC, people.name ASC").
			Scan(&actorRows).Error; err != nil {
			return nil, fmt.Errorf("load actor search migration batch: %w", err)
		}
		actorsByMediaID := make(map[string][]string, len(mediaItems))
		for _, row := range actorRows {
			actorsByMediaID[row.MediaID] = append(actorsByMediaID[row.MediaID], row.Name, row.OrigName)
		}
		for i := range mediaItems {
			media := &mediaItems[i]
			media.RefreshSearchFields(strings.Join(actorsByMediaID[media.ID], " "))
			if err := tx.Unscoped().Model(&model.Media{}).Where("id = ?", media.ID).UpdateColumns(map[string]interface{}{
				"search_text":     media.SearchText,
				"search_pinyin":   media.SearchPinyin,
				"search_initials": media.SearchInitials,
			}).Error; err != nil {
				return nil, fmt.Errorf("backfill media search fields for %s: %w", media.ID, err)
			}
			stats["media_search_rows_backfilled"]++
		}
		lastID = mediaItems[len(mediaItems)-1].ID
	}
	if err := ensureRequiredIndexes(tx); err != nil {
		return nil, err
	}
	return stats, nil
}
