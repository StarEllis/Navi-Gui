package database

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/gorm"

	"navi-desktop/model"
	"navi-desktop/service"
)

// posterImageSuffixes 是刮削器生成封面时用的后缀。它们的创建时间约等于这部片
// 第一次被刮削、也就是被整理进库的时间；重刮时 keep_files 会保住这些文件，所以
// 它比 NFO 时间更少被后续操作污染。
var posterImageSuffixes = []string{"-poster.jpg", "-thumb.jpg", "-fanart.jpg", "poster.jpg", "thumb.jpg", "fanart.jpg"}

// posterCreatedAt 返回媒体同目录下封面图里最早的创建时间。
// 网络盘上 stat 有开销，所以只看确定是封面的那几个文件名，不遍历整个目录。
func posterCreatedAt(mediaPath string) *time.Time {
	mediaPath = strings.TrimSpace(mediaPath)
	if mediaPath == "" {
		return nil
	}
	dir := filepath.Dir(mediaPath)
	stem := strings.TrimSuffix(filepath.Base(mediaPath), filepath.Ext(mediaPath))

	var earliest *time.Time
	for _, suffix := range posterImageSuffixes {
		candidate := filepath.Join(dir, stem+suffix)
		if !strings.HasPrefix(suffix, "-") {
			candidate = filepath.Join(dir, suffix)
		}
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() {
			continue
		}
		created := service.ResolveFileCreatedTime(info)
		if created == nil || created.IsZero() {
			continue
		}
		if earliest == nil || created.Before(*earliest) {
			value := created.UTC().Truncate(time.Second)
			earliest = &value
		}
	}
	return earliest
}

// resolveFrozenAddedAt 推算一条存量记录的加入时间：NFO 时间与封面图时间取较早，
// 两者都没有时退回文件时间。
func resolveFrozenAddedAt(media *model.Media) time.Time {
	var best *time.Time
	consider := func(candidate *time.Time) {
		if candidate == nil || candidate.IsZero() {
			return
		}
		if best == nil || candidate.Before(*best) {
			value := candidate.UTC().Truncate(time.Second)
			best = &value
		}
	}

	consider(media.NfoModTime)
	consider(posterCreatedAt(media.FilePath))
	if best != nil {
		return *best
	}

	// 没有 NFO 也没有封面：只能退回文件自身的时间戳。
	consider(media.FileCreatedAt)
	consider(media.FileModTime)
	if best != nil {
		return *best
	}
	if !media.CreatedAt.IsZero() {
		return media.CreatedAt.UTC().Truncate(time.Second)
	}
	return time.Now().UTC().Truncate(time.Second)
}

func migrateMediaAddedTimes(tx *gorm.DB) (MigrationStats, error) {
	stats := MigrationStats{}
	if err := tx.AutoMigrate(&model.MediaAddedTime{}); err != nil {
		return nil, fmt.Errorf("create media added times table: %w", err)
	}
	// media 上的读取副本列同样要补出来，存量库不会自己长。
	if err := tx.AutoMigrate(&model.Media{}); err != nil {
		return nil, fmt.Errorf("add library added at column: %w", err)
	}

	var media []model.Media
	if err := tx.Where("deleted_at IS NULL").Find(&media).Error; err != nil {
		return nil, fmt.Errorf("load media for added time freeze: %w", err)
	}

	for index := range media {
		item := &media[index]
		pathKey := strings.TrimSpace(item.PathKey)
		if pathKey == "" {
			pathKey = model.NormalizePathKey(item.FilePath)
		}
		if pathKey == "" {
			stats["skipped_without_path"]++
			continue
		}

		addedAt := resolveFrozenAddedAt(item)
		record := model.MediaAddedTime{
			PathKey: pathKey,
			Code:    strings.TrimSpace(item.Code),
			AddedAt: addedAt,
		}
		// 迁移只跑一次，但同一条路径重复出现时保留先写入的那个，避免顺序抖动。
		if err := tx.Where("path_key = ?", pathKey).FirstOrCreate(&record).Error; err != nil {
			return nil, fmt.Errorf("freeze added time for %s: %w", item.FilePath, err)
		}
		if err := tx.Model(&model.Media{}).Where("id = ?", item.ID).
			Update("library_added_at", record.AddedAt).Error; err != nil {
			return nil, fmt.Errorf("write cached added time for %s: %w", item.FilePath, err)
		}
		stats["frozen"]++
	}

	return stats, nil
}
