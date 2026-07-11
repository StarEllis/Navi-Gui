package player

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"navi-desktop/model"
)

type GormHistoryStore struct {
	db     *gorm.DB
	userID string
}

func NewGormHistoryStore(db *gorm.DB, userID string) *GormHistoryStore {
	return &GormHistoryStore{db: db, userID: userID}
}

func (s *GormHistoryStore) Load(ctx context.Context, mediaID string) (History, error) {
	var row model.WatchHistory
	err := s.db.WithContext(ctx).Where("user_id = ? AND media_id = ?", s.userID, mediaID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return History{MediaID: mediaID}, nil
	}
	if err != nil {
		return History{}, err
	}
	return History{MediaID: mediaID, Position: seconds(row.Position), Duration: seconds(row.Duration), Completed: row.Completed, UpdatedAt: row.UpdatedAt}, nil
}

func (s *GormHistoryStore) Save(ctx context.Context, history *History) error {
	if history == nil {
		return errors.New("nil playback history")
	}
	row := model.WatchHistory{UserID: s.userID, MediaID: history.MediaID, Position: history.Position.Seconds(), Duration: history.Duration.Seconds(), Completed: history.Completed, UpdatedAt: history.UpdatedAt}
	updates := []string{"position", "duration", "updated_at"}
	if history.Completed {
		updates = append(updates, "completed")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "user_id"}, {Name: "media_id"}},
			DoUpdates: clause.AssignmentColumns(updates),
		}).Create(&row).Error; err != nil {
			return err
		}
		var persisted model.WatchHistory
		if err := tx.Select("completed").Where("user_id = ? AND media_id = ?", s.userID, history.MediaID).First(&persisted).Error; err != nil {
			return err
		}
		history.Completed = persisted.Completed
		return nil
	})
}

func seconds(value float64) time.Duration { return time.Duration(value * float64(time.Second)) }
