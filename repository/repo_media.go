package repository

import (
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"navi-desktop/model"
)

// ==================== MediaRepo ====================

type MediaRepo struct {
	db *gorm.DB
}

type OrphanedMediaCleanupResult struct {
	MediaPeople     int64
	WatchHistories  int64
	Favorites       int64
	TranscodeTasks  int64
	PlaylistItems   int64
	Bookmarks       int64
	Comments        int64
	ContentRatings  int64
	PlaybackStats   int64
	VideoChapters   int64
	VideoHighlights int64
	AIAnalysisTasks int64
	CoverCandidates int64
	MediaTags       int64
	MediaRatings    int64
	MediaShares     int64
	MediaLikes      int64
	Recommendations int64
	ShareLinks      int64
	People          int64
}

func (r OrphanedMediaCleanupResult) Total() int64 {
	return r.MediaPeople + r.WatchHistories + r.Favorites + r.TranscodeTasks +
		r.PlaylistItems + r.Bookmarks + r.Comments + r.ContentRatings +
		r.PlaybackStats + r.VideoChapters + r.VideoHighlights + r.AIAnalysisTasks +
		r.CoverCandidates + r.MediaTags + r.MediaRatings + r.MediaShares + r.MediaLikes +
		r.Recommendations + r.ShareLinks + r.People
}

func (r *MediaRepo) deleteMediaWhere(query string, args ...interface{}) (int64, error) {
	var rowsAffected int64
	err := r.db.Transaction(func(tx *gorm.DB) error {
		var ids []string
		if err := tx.Unscoped().Model(&model.Media{}).Where(query, args...).Pluck("id", &ids).Error; err != nil {
			return err
		}

		ids = normalizeIDs(ids)
		if len(ids) == 0 {
			return nil
		}
		if _, err := deleteMediaAssociationsByIDs(tx, ids); err != nil {
			return err
		}

		result := tx.Unscoped().Where("id IN ?", ids).Delete(&model.Media{})
		rowsAffected = result.RowsAffected
		return result.Error
	})
	return rowsAffected, err
}

func normalizeIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}

	seen := make(map[string]bool, len(ids))
	normalized := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		normalized = append(normalized, id)
	}
	return normalized
}

func deleteMediaAssociationsByIDs(tx *gorm.DB, ids []string) (OrphanedMediaCleanupResult, error) {
	var result OrphanedMediaCleanupResult
	var affectedPersonIDs []string
	if err := tx.Model(&model.MediaPerson{}).
		Where("media_id IN ?", ids).
		Distinct("person_id").
		Pluck("person_id", &affectedPersonIDs).Error; err != nil {
		return result, err
	}

	deletions := []struct {
		count  *int64
		target interface{}
	}{
		{&result.MediaPeople, &model.MediaPerson{}},
		{&result.WatchHistories, &model.WatchHistory{}},
		{&result.Favorites, &model.Favorite{}},
		{&result.TranscodeTasks, &model.TranscodeTask{}},
		{&result.PlaylistItems, &model.PlaylistItem{}},
		{&result.Bookmarks, &model.Bookmark{}},
		{&result.Comments, &model.Comment{}},
		{&result.ContentRatings, &model.ContentRating{}},
		{&result.PlaybackStats, &model.PlaybackStats{}},
		{&result.VideoChapters, &model.VideoChapter{}},
		{&result.VideoHighlights, &model.VideoHighlight{}},
		{&result.AIAnalysisTasks, &model.AIAnalysisTask{}},
		{&result.CoverCandidates, &model.CoverCandidate{}},
		{&result.MediaTags, &model.MediaTag{}},
		{&result.MediaRatings, &model.MediaRating{}},
		{&result.MediaShares, &model.MediaShare{}},
		{&result.MediaLikes, &model.MediaLike{}},
		{&result.Recommendations, &model.MediaRecommendation{}},
		{&result.ShareLinks, &model.ShareLink{}},
	}

	for _, deletion := range deletions {
		count, err := deleteRowsByMediaIDs(tx, ids, deletion.target)
		if err != nil {
			return result, err
		}
		*deletion.count = count
	}

	if len(affectedPersonIDs) == 0 {
		return result, nil
	}
	people, err := deletePeopleWithoutAnyMediaPeople(tx, affectedPersonIDs)
	result.People = people
	return result, err
}

func deleteRowsByMediaIDs(tx *gorm.DB, ids []string, target interface{}) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	result := tx.Unscoped().Where("media_id IN ?", ids).Delete(target)
	return result.RowsAffected, result.Error
}

// CleanOrphanedMediaAssociations removes rows that already point at missing or soft-deleted media.
func (r *MediaRepo) CleanOrphanedMediaAssociations() (OrphanedMediaCleanupResult, error) {
	var cleaned OrphanedMediaCleanupResult
	err := r.db.Transaction(func(tx *gorm.DB) error {
		var err error
		cleaned, err = deleteOrphanedMediaAssociations(tx)
		return err
	})
	return cleaned, err
}

func deleteOrphanedMediaAssociations(tx *gorm.DB) (OrphanedMediaCleanupResult, error) {
	var result OrphanedMediaCleanupResult
	deletions := []struct {
		count  *int64
		target interface{}
	}{
		{&result.MediaPeople, &model.MediaPerson{}},
		{&result.WatchHistories, &model.WatchHistory{}},
		{&result.Favorites, &model.Favorite{}},
		{&result.TranscodeTasks, &model.TranscodeTask{}},
		{&result.PlaylistItems, &model.PlaylistItem{}},
		{&result.Bookmarks, &model.Bookmark{}},
		{&result.Comments, &model.Comment{}},
		{&result.ContentRatings, &model.ContentRating{}},
		{&result.PlaybackStats, &model.PlaybackStats{}},
		{&result.VideoChapters, &model.VideoChapter{}},
		{&result.VideoHighlights, &model.VideoHighlight{}},
		{&result.AIAnalysisTasks, &model.AIAnalysisTask{}},
		{&result.CoverCandidates, &model.CoverCandidate{}},
		{&result.MediaTags, &model.MediaTag{}},
		{&result.MediaRatings, &model.MediaRating{}},
		{&result.MediaShares, &model.MediaShare{}},
		{&result.MediaLikes, &model.MediaLike{}},
		{&result.Recommendations, &model.MediaRecommendation{}},
		{&result.ShareLinks, &model.ShareLink{}},
	}

	for _, deletion := range deletions {
		count, err := deleteOrphanedRows(tx, deletion.target)
		if err != nil {
			return result, err
		}
		*deletion.count = count
	}

	people, err := deletePeopleWithoutAnyMediaPeople(tx, nil)
	result.People = people
	return result, err
}

func deleteOrphanedRows(tx *gorm.DB, target interface{}) (int64, error) {
	result := tx.Unscoped().Where(
		"media_id IS NOT NULL AND media_id != '' AND media_id NOT IN (SELECT id FROM media WHERE deleted_at IS NULL)",
	).Delete(target)
	return result.RowsAffected, result.Error
}

func deletePeopleWithoutAnyMediaPeople(tx *gorm.DB, personIDs []string) (int64, error) {
	personIDs = normalizeIDs(personIDs)
	query := tx.Unscoped().Where("id NOT IN (SELECT person_id FROM media_people)")
	if len(personIDs) > 0 {
		query = query.Where("id IN ?", personIDs)
	}
	result := query.Delete(&model.Person{})
	return result.RowsAffected, result.Error
}

func (r *MediaRepo) Create(media *model.Media) error {
	media.PathKey = model.NormalizePathKey(media.FilePath)
	if media.PathKey == "" {
		return fmt.Errorf("media file path is empty")
	}
	result := r.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "library_id"}, {Name: "path_key"}},
		TargetWhere: clause.Where{Exprs: []clause.Expression{
			clause.Expr{SQL: "deleted_at IS NULL AND path_key <> ''"},
		}},
		DoNothing: true,
	}).Create(media)
	result = retryLegacyCreateWithoutPartialIndex(r.db, result, media)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		return RefreshMediaSearchIndex(r.db, media.ID)
	}
	var existing model.Media
	if err := r.db.Where("library_id = ? AND path_key = ?", media.LibraryID, media.PathKey).First(&existing).Error; err != nil {
		return err
	}
	media.ID = existing.ID
	return RefreshMediaSearchIndex(r.db, media.ID)
}

func (r *MediaRepo) FindByID(id string) (*model.Media, error) {
	var media model.Media
	err := r.db.First(&media, "id = ?", id).Error
	return &media, err
}

func (r *MediaRepo) FindByFilePath(filePath string) (*model.Media, error) {
	var media model.Media
	err := r.db.Where("path_key = ?", model.NormalizePathKey(filePath)).Order("created_at ASC").First(&media).Error
	return &media, err
}
func (r *MediaRepo) FindByFilePathInLibrary(libraryID, filePath string) (*model.Media, error) {
	var media model.Media
	err := r.db.Where(
		"library_id = ? AND path_key = ?",
		libraryID,
		model.NormalizePathKey(filePath),
	).First(&media).Error
	return &media, err
}

func (r *MediaRepo) List(page, size int, libraryID string) ([]model.Media, int64, error) {
	var media []model.Media
	var total int64

	query := r.db.Model(&model.Media{})
	if libraryID != "" {
		query = query.Where("library_id = ?", libraryID)
	}

	query.Count(&total)
	err := query.Order("created_at DESC").Offset((page - 1) * size).Limit(size).Find(&media).Error
	return media, total, err
}

func (r *MediaRepo) Recent(limit int) ([]model.Media, error) {
	var media []model.Media
	err := r.db.Order("created_at DESC").Limit(limit).Find(&media).Error
	return media, err
}

func (r *MediaRepo) Search(keyword string, page, size int) ([]model.Media, int64, error) {
	var media []model.Media
	var total int64

	// 改进搜索：支持多字段搜索（标题、原始标题、类型），并按相关性排序
	query := r.db.Model(&model.Media{}).Where(
		"title LIKE ? OR orig_title LIKE ? OR genres LIKE ?",
		"%"+keyword+"%", "%"+keyword+"%", "%"+keyword+"%",
	)
	query.Count(&total)
	// 优先显示标题精确匹配的结果，然后按评分降序
	err := query.Order(clause.Expr{
		SQL:  "CASE WHEN title = ? THEN 0 WHEN title LIKE ? THEN 1 ELSE 2 END, rating DESC, created_at DESC",
		Vars: []interface{}{keyword, keyword + "%"},
	}).Offset((page - 1) * size).Limit(size).Find(&media).Error
	return media, total, err
}

// SearchAdvancedParams 高级搜索参数
type SearchAdvancedParams struct {
	Keyword   string
	MediaType string
	Genre     string
	YearMin   int
	YearMax   int
	MinRating float64
	SortBy    string
	SortOrder string
	Page      int
	Size      int
}

// SearchAdvanced 高级搜索 — 支持多条件组合筛选、排序
func (r *MediaRepo) SearchAdvanced(params SearchAdvancedParams) ([]model.Media, int64, error) {
	var media []model.Media
	var total int64

	query := r.db.Model(&model.Media{})

	if params.Keyword != "" {
		// 改进：多字段搜索
		query = query.Where(
			"title LIKE ? OR orig_title LIKE ? OR tagline LIKE ?",
			"%"+params.Keyword+"%", "%"+params.Keyword+"%", "%"+params.Keyword+"%",
		)
	}
	if params.MediaType != "" {
		query = query.Where("media_type = ?", params.MediaType)
	}
	if params.Genre != "" {
		// 改进：支持多类型筛选（逗号分隔）
		genres := strings.Split(params.Genre, ",")
		for _, g := range genres {
			g = strings.TrimSpace(g)
			if g != "" {
				query = query.Where("genres LIKE ?", "%"+g+"%")
			}
		}
	}
	if params.YearMin > 0 {
		query = query.Where("year >= ?", params.YearMin)
	}
	if params.YearMax > 0 {
		query = query.Where("year <= ?", params.YearMax)
	}
	if params.MinRating > 0 {
		query = query.Where("rating >= ?", params.MinRating)
	}

	query.Count(&total)

	sortField := "COALESCE(file_created_at, file_mod_time, created_at)"
	sortDir := "DESC"
	switch params.SortBy {
	case "title":
		sortField = "title"
	case "year":
		sortField = "year"
	case "rating":
		sortField = "rating"
	case "created_at":
		sortField = "COALESCE(file_created_at, file_mod_time, created_at)"
	}
	if params.SortOrder == "asc" {
		sortDir = "ASC"
	}

	page := params.Page
	size := params.Size
	if page <= 0 {
		page = 1
	}
	if size <= 0 || size > 100 {
		size = 20
	}

	err := query.Order(fmt.Sprintf("%s %s", sortField, sortDir)).
		Offset((page - 1) * size).Limit(size).Find(&media).Error

	return media, total, err
}

func (r *MediaRepo) DeleteByID(id string) error {
	_, err := r.DeleteByIDs([]string{id})
	return err
}

func (r *MediaRepo) DeleteByLibraryID(libraryID string) error {
	_, err := r.deleteMediaWhere("library_id = ?", libraryID)
	return err
}

func (r *MediaRepo) CleanOrphanedByLibraryIDs(validLibraryIDs []string) (int64, error) {
	if len(validLibraryIDs) == 0 {
		return r.deleteMediaWhere("1 = 1")
	}
	return r.deleteMediaWhere("library_id NOT IN ?", validLibraryIDs)
}

func (r *MediaRepo) Update(media *model.Media) error {
	if media.SeriesID != "" {
		if err := r.db.Save(media).Error; err != nil {
			return err
		}
		return RefreshMediaSearchIndex(r.db, media.ID)
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Omit("SeriesID").Save(media).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.Media{}).Where("id = ?", media.ID).UpdateColumn("series_id", nil).Error; err != nil {
			return err
		}
		return RefreshMediaSearchIndex(tx, media.ID)
	})
}

func (r *MediaRepo) FindByIDs(ids []string) ([]model.Media, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var media []model.Media
	err := r.db.Where("id IN ?", ids).Find(&media).Error
	return media, err
}

func (r *MediaRepo) ListByGenres(genres []string, excludeIDs []string, limit int) ([]model.Media, error) {
	if len(genres) == 0 {
		return nil, nil
	}
	query := r.db.Model(&model.Media{})
	for i, genre := range genres {
		if i == 0 {
			query = query.Where("genres LIKE ?", "%"+genre+"%")
		} else {
			query = query.Or("genres LIKE ?", "%"+genre+"%")
		}
	}
	if len(excludeIDs) > 0 {
		query = query.Where("id NOT IN ?", excludeIDs)
	}
	var media []model.Media
	err := query.Order("rating DESC").Limit(limit).Find(&media).Error
	return media, err
}

// ListHighRated 获取高评分媒体（用于冷启动推荐的多样化内容）
func (r *MediaRepo) ListHighRated(limit int, minRating float64) ([]model.Media, error) {
	var media []model.Media
	err := r.db.Where("rating >= ?", minRating).
		Order("rating DESC, created_at DESC").
		Limit(limit).
		Find(&media).Error
	return media, err
}

func (r *MediaRepo) ListByLibraryID(libraryID string) ([]model.Media, error) {
	var media []model.Media
	err := r.db.Where("library_id = ?", libraryID).Find(&media).Error
	return media, err
}

func (r *MediaRepo) ListQuickMetadataIDs() ([]string, error) {
	var ids []string
	err := r.db.Model(&model.Media{}).
		Where("metadata_phase = ?", "quick").
		Order("created_at ASC").
		Pluck("id", &ids).Error
	return ids, err
}

func (r *MediaRepo) ListBySeriesID(seriesID string) ([]model.Media, error) {
	var media []model.Media
	err := r.db.Where("series_id = ?", seriesID).
		Order("season_num ASC, episode_num ASC").Find(&media).Error
	return media, err
}

func (r *MediaRepo) ListBySeriesAndSeason(seriesID string, seasonNum int) ([]model.Media, error) {
	var media []model.Media
	err := r.db.Where("series_id = ? AND season_num = ?", seriesID, seasonNum).
		Order("episode_num ASC").Find(&media).Error
	return media, err
}

func (r *MediaRepo) RecentNonEpisode(limit int) ([]model.Media, error) {
	var media []model.Media
	err := r.db.Where("(series_id = '' OR series_id IS NULL) AND library_id != ''").
		Order("created_at DESC").Limit(limit).Find(&media).Error
	return media, err
}

func (r *MediaRepo) RecentNonEpisodeAll(libraryID string) ([]model.Media, error) {
	var media []model.Media
	query := r.db.Where("(series_id = '' OR series_id IS NULL) AND library_id != ''")
	if libraryID != "" {
		query = query.Where("library_id = ?", libraryID)
	}
	err := query.Order("created_at DESC").Find(&media).Error
	return media, err
}

func (r *MediaRepo) ListNonEpisode(page, size int, libraryID string) ([]model.Media, int64, error) {
	var media []model.Media
	var total int64

	query := r.db.Model(&model.Media{}).Where("(series_id = '' OR series_id IS NULL) AND library_id != ''")
	if libraryID != "" {
		query = query.Where("library_id = ?", libraryID)
	}

	query.Count(&total)
	err := query.Order("created_at DESC").Offset((page - 1) * size).Limit(size).Find(&media).Error
	return media, total, err
}

func (r *MediaRepo) CleanGhostMedia() (int64, error) {
	result := r.db.Unscoped().Where("library_id = '' OR library_id IS NULL").Delete(&model.Media{})
	return result.RowsAffected, result.Error
}

func (r *MediaRepo) CountNonEpisodeByLibrary(libraryID string) (int64, error) {
	var count int64
	query := r.db.Model(&model.Media{}).Where("(series_id = '' OR series_id IS NULL) AND library_id != ''")
	if libraryID != "" {
		query = query.Where("library_id = ?", libraryID)
	}
	err := query.Count(&count).Error
	return count, err
}

func (r *MediaRepo) CountNonEpisode(libraryID string) (int64, error) {
	var count int64
	query := r.db.Model(&model.Media{}).Where("(series_id = '' OR series_id IS NULL) AND library_id != ''")
	if libraryID != "" {
		query = query.Where("library_id = ?", libraryID)
	}
	err := query.Count(&count).Error
	return count, err
}

// ==================== MediaRepo 扩展方法（文件管理） ====================

func (r *MediaRepo) ListFilesAdvanced(page, size int, libraryID, mediaType, keyword, sortBy, sortOrder string, scrapedOnly *bool) ([]model.Media, int64, error) {
	var media []model.Media
	var total int64

	query := r.db.Model(&model.Media{})

	if libraryID != "" {
		query = query.Where("library_id = ?", libraryID)
	}
	if mediaType != "" {
		query = query.Where("media_type = ?", mediaType)
	}
	if keyword != "" {
		query = query.Where("title LIKE ? OR orig_title LIKE ? OR file_path LIKE ?",
			"%"+keyword+"%", "%"+keyword+"%", "%"+keyword+"%")
	}
	if scrapedOnly != nil {
		if *scrapedOnly {
			query = query.Where("(tmdb_id > 0 OR bangumi_id > 0 OR douban_id != '')")
		} else {
			query = query.Where("tmdb_id = 0 AND bangumi_id = 0 AND (douban_id = '' OR douban_id IS NULL)")
		}
	}

	query.Count(&total)

	sortField := "COALESCE(file_created_at, file_mod_time, created_at)"
	sortDir := "DESC"
	switch sortBy {
	case "title":
		sortField = "title"
	case "year":
		sortField = "year"
	case "rating":
		sortField = "rating"
	case "file_size":
		sortField = "file_size"
	case "created_at":
		sortField = "COALESCE(file_created_at, file_mod_time, created_at)"
	case "updated_at":
		sortField = "updated_at"
	}
	if sortOrder == "asc" {
		sortDir = "ASC"
	}

	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}

	err := query.Order(fmt.Sprintf("%s %s", sortField, sortDir)).
		Offset((page - 1) * size).Limit(size).Find(&media).Error
	return media, total, err
}

func (r *MediaRepo) CountByMediaType(mediaType string) (int64, error) {
	var count int64
	err := r.db.Model(&model.Media{}).Where("media_type = ?", mediaType).Count(&count).Error
	return count, err
}

func (r *MediaRepo) CountScraped() (int64, error) {
	var count int64
	err := r.db.Model(&model.Media{}).
		Where("tmdb_id > 0 OR bangumi_id > 0 OR (douban_id != '' AND douban_id IS NOT NULL)").
		Count(&count).Error
	return count, err
}

func (r *MediaRepo) SumFileSize() (int64, error) {
	var total int64
	err := r.db.Model(&model.Media{}).Select("COALESCE(SUM(file_size), 0)").Scan(&total).Error
	return total, err
}

func (r *MediaRepo) CountRecentImports(days int) (int64, error) {
	var count int64
	err := r.db.Model(&model.Media{}).
		Where("created_at >= datetime('now', ?)", fmt.Sprintf("-%d days", days)).
		Count(&count).Error
	return count, err
}

func (r *MediaRepo) ListByMediaType(mediaType string) ([]model.Media, error) {
	var media []model.Media
	err := r.db.Where("media_type = ?", mediaType).Find(&media).Error
	return media, err
}

func (r *MediaRepo) ListMoviesMissingActorRelations() ([]model.Media, error) {
	var media []model.Media
	err := r.db.
		Select("id, library_id, file_path, media_type").
		Where("media_type = ? AND file_path <> ?", "movie", "").
		Where(`NOT EXISTS (
			SELECT 1 FROM media_people
			WHERE media_people.media_id = media.id AND media_people.role = ?
		)`, "actor").
		Order("created_at ASC").
		Find(&media).Error
	return media, err
}

func (r *MediaRepo) BatchUpdateMediaType(ids []string, mediaType string) (int64, error) {
	result := r.db.Model(&model.Media{}).Where("id IN ?", ids).Update("media_type", mediaType)
	return result.RowsAffected, result.Error
}

// GetAllFilePaths 获取所有媒体文件路径（用于构建文件夹树）
func (r *MediaRepo) GetAllFilePaths(libraryID string) ([]string, error) {
	var paths []string
	query := r.db.Model(&model.Media{}).Select("file_path")
	if libraryID != "" {
		query = query.Where("library_id = ?", libraryID)
	}
	err := query.Pluck("file_path", &paths).Error
	return paths, err
}

// ListByFolderPath 按文件夹路径查询文件（精确匹配目录，不递归子目录）
func (r *MediaRepo) ListByFolderPath(folderPath string, page, size int, libraryID, mediaType, keyword, sortBy, sortOrder string, scrapedOnly *bool) ([]model.Media, int64, error) {
	var media []model.Media
	var total int64

	query := r.db.Model(&model.Media{})

	// 使用 LIKE 匹配指定目录下的直接子文件（不含子目录中的文件）
	// folderPath 末尾需要加分隔符
	// SQLite 中使用 file_path LIKE 'folder/%' AND file_path NOT LIKE 'folder/%/%'
	if folderPath != "" {
		// 标准化路径分隔符
		normalizedPath := strings.ReplaceAll(folderPath, "\\", "/")
		if !strings.HasSuffix(normalizedPath, "/") {
			normalizedPath += "/"
		}
		query = query.Where(
			"(REPLACE(file_path, '\\', '/') LIKE ? AND REPLACE(file_path, '\\', '/') NOT LIKE ?)",
			normalizedPath+"%",
			normalizedPath+"%/%",
		)
	}

	if libraryID != "" {
		query = query.Where("library_id = ?", libraryID)
	}
	if mediaType != "" {
		query = query.Where("media_type = ?", mediaType)
	}
	if keyword != "" {
		query = query.Where("title LIKE ? OR orig_title LIKE ? OR file_path LIKE ?",
			"%"+keyword+"%", "%"+keyword+"%", "%"+keyword+"%")
	}
	if scrapedOnly != nil {
		if *scrapedOnly {
			query = query.Where("(tmdb_id > 0 OR bangumi_id > 0 OR douban_id != '')")
		} else {
			query = query.Where("tmdb_id = 0 AND bangumi_id = 0 AND (douban_id = '' OR douban_id IS NULL)")
		}
	}

	query.Count(&total)

	sortField := "COALESCE(file_created_at, file_mod_time, created_at)"
	sortDir := "DESC"
	switch sortBy {
	case "title":
		sortField = "title"
	case "year":
		sortField = "year"
	case "rating":
		sortField = "rating"
	case "file_size":
		sortField = "file_size"
	case "created_at":
		sortField = "COALESCE(file_created_at, file_mod_time, created_at)"
	case "updated_at":
		sortField = "updated_at"
	}
	if sortOrder == "asc" {
		sortDir = "ASC"
	}

	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 20
	}

	err := query.Order(fmt.Sprintf("%s %s", sortField, sortDir)).
		Offset((page - 1) * size).Limit(size).Find(&media).Error
	return media, total, err
}

// UpdateFilePathPrefix 批量更新文件路径前缀（用于文件夹重命名）
func (r *MediaRepo) UpdateFilePathPrefix(oldPrefix, newPrefix string) error {
	return r.db.Exec(
		"UPDATE media SET file_path = ? || SUBSTR(REPLACE(file_path, '\\', '/'), LENGTH(?) + 1) WHERE REPLACE(file_path, '\\', '/') LIKE ?",
		newPrefix, oldPrefix, oldPrefix+"%",
	).Error
}

// DeleteByPathPrefix 删除指定路径前缀下的所有文件记录
func (r *MediaRepo) DeleteByPathPrefix(pathPrefix string) error {
	return r.db.Where("REPLACE(file_path, '\\', '/') LIKE ?", pathPrefix+"%").Delete(&model.Media{}).Error
}

// ==================== P2/P3: 性能优化方法 ====================

// GetAllFilePathsByLibrary 获取指定媒体库的所有文件路径集合（用于内存查重，避免 N+1 查询）
func (r *MediaRepo) GetAllFilePathsByLibrary(libraryID string) (map[string]bool, error) {
	var paths []string
	err := r.db.Model(&model.Media{}).Where("library_id = ?", libraryID).Pluck("file_path", &paths).Error
	if err != nil {
		return nil, err
	}
	pathSet := make(map[string]bool, len(paths))
	for _, p := range paths {
		pathSet[p] = true
	}
	return pathSet, nil
}

type MediaFileSignature struct {
	ID                 string
	FilePath           string
	SeriesID           string
	FileSize           int64
	FileCreatedAt      *time.Time
	FileModTime        *time.Time
	VideoFingerprint   string
	SidecarFingerprint string
}

func (r *MediaRepo) GetAllFileSignaturesByLibrary(libraryID string) (map[string]MediaFileSignature, error) {
	var rows []MediaFileSignature
	err := r.db.Model(&model.Media{}).
		Select("id, file_path, series_id, file_size, file_created_at, file_mod_time, video_fingerprint, sidecar_fingerprint").
		Where("library_id = ?", libraryID).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	signatures := make(map[string]MediaFileSignature, len(rows))
	for _, row := range rows {
		signatures[row.FilePath] = row
	}
	return signatures, nil
}

// BatchCreate 批量创建媒体记录（减少 SQLite 写锁竞争，每批 100 条）
func (r *MediaRepo) BatchCreate(mediaList []*model.Media) error {
	if len(mediaList) == 0 {
		return nil
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		return tx.CreateInBatches(mediaList, 100).Error
	})
}

// UpdateFields 仅更新指定字段（减少写锁争用，提高 SQLite 并发性能）
func (r *MediaRepo) UpdateFields(id string, fields map[string]interface{}) error {
	if err := r.db.Model(&model.Media{}).Where("id = ?", id).Updates(fields).Error; err != nil {
		return err
	}
	if mediaSearchFieldsChanged(fields) {
		return RefreshMediaSearchIndex(r.db, id)
	}
	return nil
}

// DeleteByIDs 批量删除指定 ID 的媒体记录（用于清理已删除文件）
func (r *MediaRepo) DeleteByIDs(ids []string) (int64, error) {
	ids = normalizeIDs(ids)
	if len(ids) == 0 {
		return 0, nil
	}
	return r.deleteMediaWhere("id IN ?", ids)
}

const mediaDeleteBatchSize = 100

// DeleteByIDsAndRepairSeries atomically deletes media associations and media rows,
// then repairs or removes every affected series in the same transaction.
func (r *MediaRepo) DeleteByIDsAndRepairSeries(ids []string) (int64, error) {
	ids = normalizeIDs(ids)
	if len(ids) == 0 {
		return 0, nil
	}

	var totalDeleted int64
	err := r.db.Transaction(func(tx *gorm.DB) error {
		affectedSeries := make(map[string]bool)

		for i := 0; i < len(ids); i += mediaDeleteBatchSize {
			end := i + mediaDeleteBatchSize
			if end > len(ids) {
				end = len(ids)
			}
			batch := ids[i:end]

			var seriesIDs []string
			if err := tx.Model(&model.Media{}).
				Where("id IN ? AND series_id IS NOT NULL AND series_id != ''", batch).
				Distinct("series_id").
				Pluck("series_id", &seriesIDs).Error; err != nil {
				return fmt.Errorf("load affected series: %w", err)
			}
			for _, seriesID := range seriesIDs {
				if seriesID = strings.TrimSpace(seriesID); seriesID != "" {
					affectedSeries[seriesID] = true
				}
			}

			if _, err := deleteMediaAssociationsByIDs(tx, batch); err != nil {
				return fmt.Errorf("delete media associations: %w", err)
			}
			deleted := tx.Unscoped().Where("id IN ?", batch).Delete(&model.Media{})
			if deleted.Error != nil {
				return fmt.Errorf("delete media rows: %w", deleted.Error)
			}
			totalDeleted += deleted.RowsAffected
		}

		for seriesID := range affectedSeries {
			var episodeCount int64
			if err := tx.Model(&model.Media{}).Where("series_id = ?", seriesID).Count(&episodeCount).Error; err != nil {
				return fmt.Errorf("count series episodes %s: %w", seriesID, err)
			}

			if episodeCount == 0 {
				if err := deleteLibraryScrapeRows(tx, nil, []string{seriesID}); err != nil {
					return fmt.Errorf("delete empty series scrape rows %s: %w", seriesID, err)
				}
				if err := deleteLibrarySeriesAssociations(tx, []string{seriesID}); err != nil {
					return fmt.Errorf("delete empty series associations %s: %w", seriesID, err)
				}
				deleted := tx.Unscoped().Where("id = ?", seriesID).Delete(&model.Series{})
				if deleted.Error != nil {
					return fmt.Errorf("delete empty series %s: %w", seriesID, deleted.Error)
				}
				if deleted.RowsAffected == 0 {
					return fmt.Errorf("delete empty series %s: %w", seriesID, gorm.ErrRecordNotFound)
				}
				continue
			}

			var seasonNumbers []int
			if err := tx.Model(&model.Media{}).
				Where("series_id = ?", seriesID).
				Distinct("season_num").
				Pluck("season_num", &seasonNumbers).Error; err != nil {
				return fmt.Errorf("count series seasons %s: %w", seriesID, err)
			}
			updated := tx.Model(&model.Series{}).
				Where("id = ?", seriesID).
				Updates(map[string]interface{}{
					"episode_count": int(episodeCount),
					"season_count":  len(seasonNumbers),
				})
			if updated.Error != nil {
				return fmt.Errorf("update series counters %s: %w", seriesID, updated.Error)
			}
			if updated.RowsAffected == 0 {
				return fmt.Errorf("update series counters %s: %w", seriesID, gorm.ErrRecordNotFound)
			}
		}

		return nil
	})
	if err != nil {
		return 0, err
	}
	return totalDeleted, nil
}

// MediaPathRecord 媒体文件路径记录（轻量结构，仅包含 ID、路径和关联的 SeriesID）
type MediaPathRecord struct {
	ID       string
	FilePath string
	SeriesID string
}

// ListIDAndPathByLibrary 获取指定媒体库的所有媒体 ID、文件路径和 SeriesID（用于清理已删除文件）
func (r *MediaRepo) ListIDAndPathByLibrary(libraryID string) ([]MediaPathRecord, error) {
	var records []MediaPathRecord
	err := r.db.Model(&model.Media{}).
		Select("id, file_path, series_id").
		Where("library_id = ?", libraryID).
		Scan(&records).Error
	return records, err
}

// ListNeedScrape 获取需要刮削的媒体列表（P3: 排除最近 N 天内已失败的记录）
func (r *MediaRepo) ListNeedScrape(libraryID string, skipRecentFailedDays int) ([]model.Media, error) {
	var media []model.Media
	query := r.db.Model(&model.Media{}).Where(
		"(overview = '' OR poster_path = '') AND scrape_status != 'manual'",
	)
	if libraryID != "" {
		query = query.Where("library_id = ?", libraryID)
	}
	// P3: 跳过最近 N 天内已尝试刮削但失败的记录（避免重复无效请求）
	if skipRecentFailedDays > 0 {
		query = query.Where(
			"NOT (scrape_status = 'failed' AND last_scrape_at >= datetime('now', ?))",
			fmt.Sprintf("-%d days", skipRecentFailedDays),
		)
	}
	err := query.Order("created_at DESC").Find(&media).Error
	return media, err
}

func (r *MediaRepo) FindRunnableThumbnailTasks(limit int, lockTimeout time.Duration) ([]model.Media, error) {
	if limit <= 0 {
		limit = 10
	}

	var media []model.Media
	now := time.Now().UTC()
	lockCutoff := now.Add(-lockTimeout)

	err := r.db.Model(&model.Media{}).
		Where("thumbnail_status IN ?", []string{"pending", "stale"}).
		Where("(thumbnail_locked_at IS NULL OR thumbnail_locked_at < ?)", lockCutoff).
		Where("(thumbnail_next_attempt IS NULL OR thumbnail_next_attempt <= ?)", now).
		Order("CASE WHEN thumbnail_status = 'stale' THEN 1 ELSE 2 END").
		Order("COALESCE(thumbnail_updated_at, created_at) ASC").
		Limit(limit).
		Find(&media).Error

	return media, err
}

func (r *MediaRepo) LockThumbnailTask(mediaID string, workerID string, lockTimeout time.Duration) (bool, error) {
	now := time.Now().UTC().Truncate(time.Second)
	lockCutoff := now.Add(-lockTimeout)

	result := r.db.Model(&model.Media{}).
		Where("id = ?", mediaID).
		Where("thumbnail_status IN ?", []string{"pending", "stale"}).
		Where("(thumbnail_locked_at IS NULL OR thumbnail_locked_at < ?)", lockCutoff).
		Updates(map[string]interface{}{
			"thumbnail_status":    "processing",
			"thumbnail_locked_at": &now,
			"thumbnail_locked_by": workerID,
		})

	return result.RowsAffected > 0, result.Error
}

func (r *MediaRepo) UpdateThumbnailStatus(mediaID string, fields map[string]interface{}) error {
	if strings.TrimSpace(mediaID) == "" || len(fields) == 0 {
		return nil
	}
	return r.db.Model(&model.Media{}).Where("id = ?", mediaID).Updates(fields).Error
}

func (r *MediaRepo) RetryThumbnailTask(mediaID string) (bool, error) {
	mediaID = strings.TrimSpace(mediaID)
	if mediaID == "" {
		return false, nil
	}
	result := r.db.Model(&model.Media{}).
		Where("id = ?", mediaID).
		Where("thumbnail_status IN ?", []string{"failed", "partial", "canceled"}).
		Updates(map[string]interface{}{
			"thumbnail_status":       "pending",
			"thumbnail_retry_count":  0,
			"thumbnail_next_attempt": nil,
			"thumbnail_locked_at":    nil,
			"thumbnail_locked_by":    "",
			"thumbnail_error":        "",
		})
	return result.RowsAffected > 0, result.Error
}

func (r *MediaRepo) PromoteFailedThumbnailTasks() (int64, error) {
	now := time.Now().UTC().Truncate(time.Second)
	result := r.db.Model(&model.Media{}).
		Where("thumbnail_status IN ?", []string{"failed", "partial"}).
		Where("thumbnail_retry_count < ?", 5).
		Where("thumbnail_next_attempt IS NOT NULL").
		Where("thumbnail_next_attempt <= ?", now).
		Updates(map[string]interface{}{
			"thumbnail_status":    "pending",
			"thumbnail_locked_at": nil,
			"thumbnail_locked_by": "",
		})

	return result.RowsAffected, result.Error
}

func (r *MediaRepo) RecoverStalledThumbnailTasks(lockTimeout time.Duration) (int64, error) {
	now := time.Now().UTC().Truncate(time.Second)
	lockCutoff := now.Add(-lockTimeout)

	result := r.db.Model(&model.Media{}).
		Where("thumbnail_status = ?", "processing").
		Where("(thumbnail_locked_at IS NULL OR thumbnail_locked_at < ?)", lockCutoff).
		Updates(map[string]interface{}{
			"thumbnail_status":    "stale",
			"thumbnail_locked_at": nil,
			"thumbnail_locked_by": "",
		})

	return result.RowsAffected, result.Error
}
