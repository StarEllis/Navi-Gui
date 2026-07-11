package repository

import (
	"strings"

	"gorm.io/gorm"
	"navi-desktop/model"
)

func RefreshMediaSearchIndex(db *gorm.DB, mediaID string) error {
	if db == nil || strings.TrimSpace(mediaID) == "" {
		return nil
	}
	var media model.Media
	if err := db.First(&media, "id = ?", mediaID).Error; err != nil {
		return err
	}
	var actors []struct {
		Name     string
		OrigName string
	}
	if err := db.Table("people").
		Select("people.name, people.orig_name").
		Joins("JOIN media_people ON media_people.person_id = people.id").
		Where("media_people.media_id = ? AND media_people.role = ?", mediaID, "actor").
		Order("media_people.sort_order ASC, people.name ASC").
		Scan(&actors).Error; err != nil {
		return err
	}
	actorNames := make([]string, 0, len(actors)*2)
	for _, actor := range actors {
		actorNames = append(actorNames, actor.Name, actor.OrigName)
	}
	media.RefreshSearchFields(strings.Join(actorNames, " "))
	return db.Model(&model.Media{}).Where("id = ?", mediaID).UpdateColumns(map[string]interface{}{
		"search_text":     media.SearchText,
		"search_pinyin":   media.SearchPinyin,
		"search_initials": media.SearchInitials,
	}).Error
}

func mediaSearchFieldsChanged(fields map[string]interface{}) bool {
	for _, key := range []string{
		"title", "orig_title", "episode_title", "code", "maker", "label", "studio", "genres",
		"release_date_normalized", "file_path", "nfo_extra_fields", "year",
	} {
		if _, ok := fields[key]; ok {
			return true
		}
	}
	return false
}
