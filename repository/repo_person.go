package repository

import (
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
	var person model.Person
	err := r.db.Where("name = ?", name).First(&person).Error
	return &person, err
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
