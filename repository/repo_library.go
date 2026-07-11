package repository

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"navi-desktop/model"
)

// ==================== LibraryRepo ====================

type LibraryRepo struct {
	db *gorm.DB
}

func (r *LibraryRepo) Create(lib *model.Library) error {
	lib.PathKey = model.LibraryPathKey(lib.Path)
	if lib.PathKey == "" {
		return fmt.Errorf("library path is empty")
	}
	result := r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "path_key"}},
		TargetWhere: clause.Where{Exprs: []clause.Expression{
			clause.Expr{SQL: "deleted_at IS NULL AND path_key <> ''"},
		}},
		DoNothing: true,
	}).Create(lib)
	result = retryLegacyCreateWithoutPartialIndex(r.db, result, lib)
	if result.Error != nil || result.RowsAffected > 0 {
		return result.Error
	}
	var existing model.Library
	if err := r.db.Where("path_key = ?", lib.PathKey).First(&existing).Error; err != nil {
		return err
	}
	lib.ID = existing.ID
	return nil
}

func (r *LibraryRepo) FindByID(id string) (*model.Library, error) {
	var lib model.Library
	err := r.db.First(&lib, "id = ?", id).Error
	return &lib, err
}

func (r *LibraryRepo) List() ([]model.Library, error) {
	var libs []model.Library
	err := r.db.Find(&libs).Error
	return libs, err
}

func (r *LibraryRepo) Update(lib *model.Library) error {
	return r.db.Save(lib).Error
}

func (r *LibraryRepo) Delete(id string) error {
	return r.db.Delete(&model.Library{}, "id = ?", id).Error
}

// DeleteLibraryResult contains the media IDs whose application cache may be
// removed after the database transaction has committed.
type DeleteLibraryResult struct {
	MediaIDs []string
}

// DeleteLibraryAtomic removes a library and all database rows owned by its
// media and series in one transaction. It deliberately does not touch files or
// application caches; callers may do that only after this method succeeds.
func (r *Repositories) DeleteLibraryAtomic(libraryID string) (DeleteLibraryResult, error) {
	var result DeleteLibraryResult
	libraryID = strings.TrimSpace(libraryID)
	if libraryID == "" {
		return result, fmt.Errorf("library id is empty")
	}

	err := r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Unscoped().Model(&model.Media{}).
			Where("library_id = ?", libraryID).
			Pluck("id", &result.MediaIDs).Error; err != nil {
			return fmt.Errorf("load library media: %w", err)
		}
		result.MediaIDs = normalizeIDs(result.MediaIDs)

		var seriesIDs []string
		if err := tx.Unscoped().Model(&model.Series{}).
			Where("library_id = ?", libraryID).
			Pluck("id", &seriesIDs).Error; err != nil {
			return fmt.Errorf("load library series: %w", err)
		}
		seriesIDs = normalizeIDs(seriesIDs)

		if err := deleteLibraryScrapeRows(tx, result.MediaIDs, seriesIDs); err != nil {
			return err
		}
		if len(result.MediaIDs) > 0 {
			if _, err := deleteMediaAssociationsByIDs(tx, result.MediaIDs); err != nil {
				return fmt.Errorf("delete media associations: %w", err)
			}
			if err := tx.Unscoped().Where("id IN ?", result.MediaIDs).Delete(&model.Media{}).Error; err != nil {
				return fmt.Errorf("delete media rows: %w", err)
			}
		}
		if err := deleteLibrarySeriesAssociations(tx, seriesIDs); err != nil {
			return err
		}
		if err := tx.Unscoped().Where("library_id = ?", libraryID).Delete(&model.Series{}).Error; err != nil {
			return fmt.Errorf("delete series rows: %w", err)
		}
		if err := tx.Unscoped().Where("id = ?", libraryID).Delete(&model.Library{}).Error; err != nil {
			return fmt.Errorf("delete library row: %w", err)
		}
		return nil
	})
	if err != nil {
		return DeleteLibraryResult{}, err
	}
	return result, nil
}

func deleteLibraryScrapeRows(tx *gorm.DB, mediaIDs, seriesIDs []string) error {
	query := tx.Model(&model.ScrapeTask{})
	switch {
	case len(mediaIDs) > 0 && len(seriesIDs) > 0:
		query = query.Where("media_id IN ? OR series_id IN ?", mediaIDs, seriesIDs)
	case len(mediaIDs) > 0:
		query = query.Where("media_id IN ?", mediaIDs)
	case len(seriesIDs) > 0:
		query = query.Where("series_id IN ?", seriesIDs)
	default:
		return nil
	}

	var taskIDs []string
	if err := query.Pluck("id", &taskIDs).Error; err != nil {
		return fmt.Errorf("load scrape tasks: %w", err)
	}
	if len(taskIDs) == 0 {
		return nil
	}
	if err := tx.Where("task_id IN ?", taskIDs).Delete(&model.ScrapeHistory{}).Error; err != nil {
		return fmt.Errorf("delete scrape histories: %w", err)
	}
	if err := tx.Where("id IN ?", taskIDs).Delete(&model.ScrapeTask{}).Error; err != nil {
		return fmt.Errorf("delete scrape tasks: %w", err)
	}
	return nil
}

func deleteLibrarySeriesAssociations(tx *gorm.DB, seriesIDs []string) error {
	if len(seriesIDs) == 0 {
		return nil
	}

	var personIDs []string
	if err := tx.Model(&model.MediaPerson{}).
		Where("series_id IN ?", seriesIDs).
		Distinct("person_id").
		Pluck("person_id", &personIDs).Error; err != nil {
		return fmt.Errorf("load series people: %w", err)
	}

	for _, target := range []interface{}{
		&model.MediaPerson{},
		&model.MediaShare{},
		&model.MediaLike{},
		&model.MediaRecommendation{},
		&model.ShareLink{},
	} {
		if err := tx.Where("series_id IN ?", seriesIDs).Delete(target).Error; err != nil {
			return fmt.Errorf("delete series association %T: %w", target, err)
		}
	}
	if _, err := deletePeopleWithoutAnyMediaPeople(tx, normalizeIDs(personIDs)); err != nil {
		return fmt.Errorf("delete orphaned series people: %w", err)
	}
	return nil
}
