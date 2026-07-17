package repository

import (
	"fmt"

	"gorm.io/gorm"
	"navi-desktop/model"
)

// ==================== PersonRepo ====================

type PersonRepo struct {
	db *gorm.DB
}

func (r *PersonRepo) Create(person *model.Person) error {
	return r.db.Create(person).Error
}

func (r *PersonRepo) Update(person *model.Person) error {
	return r.db.Save(person).Error
}

func (r *PersonRepo) FindByID(id string) (*model.Person, error) {
	var person model.Person
	err := r.db.First(&person, "id = ?", id).Error
	return &person, err
}

func (r *PersonRepo) FindByTMDbID(tmdbID int) (*model.Person, error) {
	var person model.Person
	err := r.db.Where("tmdb_id = ?", tmdbID).First(&person).Error
	return &person, err
}

func (r *PersonRepo) FindByName(name string) (*model.Person, error) {
	aliases := model.EquivalentPersonNames(name)
	if len(aliases) == 0 {
		return &model.Person{}, gorm.ErrRecordNotFound
	}
	var people []model.Person
	err := r.db.Where("name IN ?", aliases).
		Order("created_at ASC").
		Order("id ASC").
		Find(&people).Error
	if err != nil {
		return &model.Person{}, err
	}
	if len(people) > 0 {
		return &people[0], nil
	}

	canonicalName := aliases[0]
	if err := r.db.Order("created_at ASC").Order("id ASC").Find(&people).Error; err != nil {
		return &model.Person{}, err
	}
	for i := range people {
		if model.NormalizeChineseVariants(people[i].Name) == canonicalName {
			return &people[i], nil
		}
	}
	return &model.Person{}, gorm.ErrRecordNotFound
}

func (r *PersonRepo) FindOrCreate(name string, tmdbID int) (*model.Person, error) {
	if tmdbID > 0 {
		person, err := r.FindByTMDbID(tmdbID)
		if err == nil {
			return person, nil
		}
	}
	person, err := r.FindByName(name)
	if err == nil {
		return person, nil
	}
	newPerson := &model.Person{Name: name, TMDbID: tmdbID}
	if err := r.Create(newPerson); err != nil {
		return nil, err
	}
	return newPerson, nil
}

type EquivalentPersonMergeStats struct {
	PeopleMerged       int
	RelationsMoved     int64
	DuplicateRelations int64
	MediaReindexed     int
}

func (r *PersonRepo) MergeEquivalentPeople() (EquivalentPersonMergeStats, error) {
	var stats EquivalentPersonMergeStats
	var people []model.Person
	if err := r.db.Order("created_at ASC").Order("id ASC").Find(&people).Error; err != nil {
		return stats, err
	}

	type personMerge struct {
		PreferredID string
		DuplicateID string
	}
	preferredByName := make(map[string]string, len(people))
	var merges []personMerge
	for _, person := range people {
		key := model.NormalizeChineseVariants(person.Name)
		if key == "" {
			continue
		}
		preferredID, exists := preferredByName[key]
		if !exists {
			preferredByName[key] = person.ID
			continue
		}
		if preferredID != person.ID {
			merges = append(merges, personMerge{PreferredID: preferredID, DuplicateID: person.ID})
		}
	}
	if len(merges) == 0 {
		return stats, nil
	}

	err := r.db.Transaction(func(tx *gorm.DB) error {
		mediaToReindex := make(map[string]bool)
		for _, merge := range merges {
			var mediaIDs []string
			if err := tx.Table("media_people").
				Distinct("media_id").
				Where("person_id = ? AND media_id <> ?", merge.DuplicateID, "").
				Pluck("media_id", &mediaIDs).Error; err != nil {
				return fmt.Errorf("load duplicate person media: %w", err)
			}
			for _, mediaID := range mediaIDs {
				mediaToReindex[mediaID] = true
			}

			deleted := tx.Exec(`
				DELETE FROM media_people
				WHERE person_id = ?
				  AND EXISTS (
					SELECT 1 FROM media_people AS preferred
					WHERE preferred.person_id = ?
					  AND COALESCE(preferred.media_id, '') = COALESCE(media_people.media_id, '')
					  AND COALESCE(preferred.series_id, '') = COALESCE(media_people.series_id, '')
					  AND preferred.role = media_people.role
					  AND COALESCE(preferred.character, '') = COALESCE(media_people.character, '')
				  )
			`, merge.DuplicateID, merge.PreferredID)
			if deleted.Error != nil {
				return fmt.Errorf("delete duplicate person relations: %w", deleted.Error)
			}
			stats.DuplicateRelations += deleted.RowsAffected

			moved := tx.Model(&model.MediaPerson{}).
				Where("person_id = ?", merge.DuplicateID).
				Update("person_id", merge.PreferredID)
			if moved.Error != nil {
				return fmt.Errorf("move duplicate person relations: %w", moved.Error)
			}
			stats.RelationsMoved += moved.RowsAffected

			if err := tx.Delete(&model.Person{}, "id = ?", merge.DuplicateID).Error; err != nil {
				return fmt.Errorf("delete duplicate person: %w", err)
			}
			stats.PeopleMerged++
		}

		for mediaID := range mediaToReindex {
			if err := RefreshMediaSearchIndex(tx, mediaID); err != nil {
				return fmt.Errorf("refresh merged person media search index: %w", err)
			}
			stats.MediaReindexed++
		}
		return nil
	})
	return stats, err
}

func (r *PersonRepo) Search(keyword string, limit int) ([]model.Person, error) {
	var people []model.Person
	err := r.db.Where("name LIKE ?", "%"+keyword+"%").Limit(limit).Find(&people).Error
	return people, err
}

// ==================== MediaPersonRepo ====================

type MediaPersonRepo struct {
	db *gorm.DB
}

func (r *MediaPersonRepo) Create(mp *model.MediaPerson) error {
	return r.db.Create(mp).Error
}

func (r *MediaPersonRepo) ListByMediaID(mediaID string) ([]model.MediaPerson, error) {
	var mps []model.MediaPerson
	err := r.db.Preload("Person").Where("media_id = ?", mediaID).
		Order("role ASC, sort_order ASC").Find(&mps).Error
	return mps, err
}

func (r *MediaPersonRepo) ListBySeriesID(seriesID string) ([]model.MediaPerson, error) {
	var mps []model.MediaPerson
	err := r.db.Preload("Person").Where("series_id = ?", seriesID).
		Order("role ASC, sort_order ASC").Find(&mps).Error
	return mps, err
}

func (r *MediaPersonRepo) DeleteByMediaID(mediaID string) error {
	return r.db.Where("media_id = ?", mediaID).Delete(&model.MediaPerson{}).Error
}

func (r *MediaPersonRepo) DeleteByMediaIDAndRole(mediaID, role string) error {
	return r.db.Where("media_id = ? AND role = ?", mediaID, role).Delete(&model.MediaPerson{}).Error
}

func (r *MediaPersonRepo) DeleteBySeriesID(seriesID string) error {
	return r.db.Where("series_id = ?", seriesID).Delete(&model.MediaPerson{}).Error
}

func (r *MediaPersonRepo) RefreshMediaSearchIndex(mediaID string) error {
	return RefreshMediaSearchIndex(r.db, mediaID)
}

func (r *MediaPersonRepo) ListByPersonID(personID string) ([]model.MediaPerson, error) {
	var mps []model.MediaPerson
	err := r.db.Where("person_id = ?", personID).Find(&mps).Error
	return mps, err
}
