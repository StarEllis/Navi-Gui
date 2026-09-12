package repository

import (
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"navi-desktop/model"
)

// ResolveAddedAt 决定一条媒体的「加入时间」，并保证同一部片无论怎么改名、
// 怎么被覆盖扫描重建，拿到的都是同一个时间。
//
// 三层匹配，逐层放宽：
//  1. 路径命中 —— 没动过的片子走这条，最可靠
//  2. 番号命中 —— 刮削器改名后路径对不上，靠番号把原来的时间追回来
//  3. 都没命中 —— 确实是新片，记下当前时刻
//
// 只有第 3 种情况会写入新时间；前两种一律沿用旧值，这是「冻结」的全部含义。
func (r *MediaRepo) ResolveAddedAt(media *model.Media, now time.Time) (time.Time, error) {
	if media == nil {
		return now, nil
	}
	pathKey := strings.TrimSpace(media.PathKey)
	if pathKey == "" {
		pathKey = model.NormalizePathKey(media.FilePath)
	}
	code := strings.TrimSpace(media.Code)
	now = now.UTC().Truncate(time.Second)

	if pathKey != "" {
		var byPath model.MediaAddedTime
		err := r.db.Where("path_key = ?", pathKey).First(&byPath).Error
		if err == nil {
			// 番号可能是这次扫描才补全的，顺手补上，让后续改名也能追回来。
			if code != "" && strings.TrimSpace(byPath.Code) != code {
				r.db.Model(&model.MediaAddedTime{}).Where("path_key = ?", pathKey).Update("code", code)
			}
			return byPath.AddedAt, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return now, err
		}
	}

	if code != "" {
		// 同番号可能有多个版本，取最早的那个，避免不同版本互相把时间推后。
		var byCode model.MediaAddedTime
		err := r.db.Where("code = ?", code).Order("added_at ASC").First(&byCode).Error
		if err == nil {
			if pathKey != "" {
				record := model.MediaAddedTime{PathKey: pathKey, Code: code, AddedAt: byCode.AddedAt}
				if createErr := r.db.Where("path_key = ?", pathKey).FirstOrCreate(&record).Error; createErr != nil {
					return byCode.AddedAt, createErr
				}
			}
			return byCode.AddedAt, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return now, err
		}
	}

	if pathKey == "" {
		return now, nil
	}
	record := model.MediaAddedTime{PathKey: pathKey, Code: code, AddedAt: now}
	if err := r.db.Where("path_key = ?", pathKey).FirstOrCreate(&record).Error; err != nil {
		return now, err
	}
	return record.AddedAt, nil
}

// ForgetAddedAt 只在用户手动移除条目时调用：那是「我不要这部片了」的意思，
// 存根一并清掉，将来重新加回来才会被当成真正的新片。
//
// 扫描发现文件不见时【不能】调用它——网络盘掉线、Everything 索引没建好都会
// 造成误判（曾经一次性误删过 298 条），存根留着，文件回来时加入时间原样恢复。
func (r *MediaRepo) ForgetAddedAt(media *model.Media) error {
	if media == nil {
		return nil
	}
	pathKey := strings.TrimSpace(media.PathKey)
	if pathKey == "" {
		pathKey = model.NormalizePathKey(media.FilePath)
	}
	if pathKey == "" {
		return nil
	}
	return r.db.Where("path_key = ?", pathKey).Delete(&model.MediaAddedTime{}).Error
}
