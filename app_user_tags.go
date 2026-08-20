package main

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"navi-desktop/model"
	"navi-desktop/service"
)

// ==================== 我的评分 ====================

// UserFilter 「我的评分 / 我的标签」组合条件。
type UserFilter struct {
	// Scores 是选中的星级，元素取值 1-5，0 表示「未评分」。
	// 星级之间是「或」：[5,4] 等价于四星以上，[5,3] 就只要五星和三星。
	// 空切片 = 不限。
	Scores    []int      `json:"scores"`
	TagGroups [][]string `json:"tag_groups"` // 元素是 tag_id：组内 OR，组间 AND
}

// splitScores 把星级拆成「评过的分数」和「要不要带上未评分」。
func splitScores(scores []int) ([]int, bool) {
	rated := make([]int, 0, len(scores))
	includeUnrated := false
	seen := make(map[int]bool, len(scores))
	for _, score := range scores {
		if seen[score] {
			continue
		}
		seen[score] = true
		switch {
		case score == 0:
			includeUnrated = true
		case score >= 1 && score <= 5:
			rated = append(rated, score)
		}
	}
	return rated, includeUnrated
}

// loadMediaUserSets 批量取出本页所有媒体的我的评分和我的标签，避免逐条查询。
func (a *App) loadMediaUserSets(mediaIDs []string) (map[string]int, map[string][]model.Tag) {
	ratingSet := make(map[string]int, len(mediaIDs))
	tagSet := make(map[string][]model.Tag, len(mediaIDs))
	if len(mediaIDs) == 0 || a.db == nil {
		return ratingSet, tagSet
	}

	var ratings []model.MediaRating
	if err := a.db.Model(&model.MediaRating{}).
		Where("user_id = ? AND media_id IN ?", desktopUserID, mediaIDs).
		Find(&ratings).Error; err != nil {
		a.logger.Warnf("load my ratings failed: %v", err)
	} else {
		for _, rating := range ratings {
			if rating.Score > 0 {
				ratingSet[rating.MediaID] = rating.Score
			}
		}
	}

	var links []model.MediaTag
	if err := a.db.Preload("Tag").
		Where("media_id IN ?", mediaIDs).
		Order("created_at ASC").
		Find(&links).Error; err != nil {
		a.logger.Warnf("load my tags failed: %v", err)
	} else {
		for _, link := range links {
			if link.Tag.ID == "" {
				continue
			}
			tagSet[link.MediaID] = append(tagSet[link.MediaID], link.Tag)
		}
	}

	return ratingSet, tagSet
}

// emptyTagsIfNil 让「没有标签」在 JSON 里是 []，不是 null。
// 前端要靠这个区分「这部一个标签都没有」和「这次没带标签信息」，
// 否则摘掉最后一个标签之后缓存永远清不掉。
func emptyTagsIfNil(tags []model.Tag) []model.Tag {
	if tags == nil {
		return []model.Tag{}
	}
	return tags
}

func (a *App) broadcastMediaUserState(mediaID string, rating *int, tags *[]model.Tag) {
	if a == nil || a.eventHub == nil || strings.TrimSpace(mediaID) == "" {
		return
	}
	a.eventHub.BroadcastEvent(service.EventMediaStateUpdated, service.MediaStateEventData{
		MediaID:  mediaID,
		MyRating: rating,
		MyTags:   tags,
		Revision: a.mediaStateRevision.Add(1),
	})
}

// SetMyRating 打分。score <= 0 或与当前分数相同时删行（点同一颗星 = 取消）。
func (a *App) SetMyRating(mediaID string, score int) error {
	mediaID = strings.TrimSpace(mediaID)
	if mediaID == "" {
		return errors.New("media id is required")
	}
	if a.db == nil {
		return errors.New("application database is not initialized")
	}
	if score > 5 {
		score = 5
	}

	var existing model.MediaRating
	err := a.db.Where("user_id = ? AND media_id = ?", desktopUserID, mediaID).First(&existing).Error
	switch {
	case err == nil:
		if score <= 0 || score == existing.Score {
			if err := a.db.Delete(&existing).Error; err != nil {
				return err
			}
			a.broadcastMediaUserState(mediaID, intPtr(0), nil)
			return nil
		}
		existing.Score = score
		existing.UpdatedAt = time.Now()
		if err := a.db.Save(&existing).Error; err != nil {
			return err
		}
		a.broadcastMediaUserState(mediaID, intPtr(score), nil)
		return nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		if score <= 0 {
			return nil
		}
		rating := model.MediaRating{UserID: desktopUserID, MediaID: mediaID, Score: score}
		if err := a.db.Create(&rating).Error; err != nil {
			return err
		}
		a.broadcastMediaUserState(mediaID, intPtr(score), nil)
		return nil
	default:
		return err
	}
}

// BatchSetMyRating 多选批量打分。score <= 0 表示清空。
func (a *App) BatchSetMyRating(mediaIDs []string, score int) error {
	for _, mediaID := range mediaIDs {
		mediaID = strings.TrimSpace(mediaID)
		if mediaID == "" {
			continue
		}
		if score <= 0 {
			if err := a.db.Where("user_id = ? AND media_id = ?", desktopUserID, mediaID).
				Delete(&model.MediaRating{}).Error; err != nil {
				return err
			}
			a.broadcastMediaUserState(mediaID, intPtr(0), nil)
			continue
		}
		// 批量场景下同分不当作取消，否则勾了 12 部按五星会把已经是五星的那几部清掉
		var existing model.MediaRating
		err := a.db.Where("user_id = ? AND media_id = ?", desktopUserID, mediaID).First(&existing).Error
		if err == nil {
			if existing.Score != score {
				existing.Score = score
				existing.UpdatedAt = time.Now()
				if err := a.db.Save(&existing).Error; err != nil {
					return err
				}
			}
		} else if errors.Is(err, gorm.ErrRecordNotFound) {
			rating := model.MediaRating{UserID: desktopUserID, MediaID: mediaID, Score: score}
			if err := a.db.Create(&rating).Error; err != nil {
				return err
			}
		} else {
			return err
		}
		a.broadcastMediaUserState(mediaID, intPtr(score), nil)
	}
	return nil
}

// GetMyRating 读取单部片子的评分，未评分返回 0。
func (a *App) GetMyRating(mediaID string) (int, error) {
	var rating model.MediaRating
	err := a.db.Where("user_id = ? AND media_id = ?", desktopUserID, strings.TrimSpace(mediaID)).First(&rating).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return rating.Score, nil
}

func intPtr(value int) *int {
	return &value
}

// ==================== 我的标签 ====================

// TagWithCount 标签及其真实使用数（media_tags 里的行数，不依赖冗余的 usage_count）。
type TagWithCount struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Count    int    `json:"count"`
}

// ListMyTags 列出全部标签，按分组、使用数、名称排序，未分组（Category == ""）排在最后。
func (a *App) ListMyTags() ([]TagWithCount, error) {
	if a.db == nil {
		return nil, errors.New("application database is not initialized")
	}
	var tags []model.Tag
	if err := a.db.Model(&model.Tag{}).Find(&tags).Error; err != nil {
		return nil, err
	}

	counts, err := a.tagUsageCounts()
	if err != nil {
		return nil, err
	}

	result := make([]TagWithCount, 0, len(tags))
	for _, tag := range tags {
		result = append(result, TagWithCount{
			ID:       tag.ID,
			Name:     tag.Name,
			Category: tag.Category,
			Count:    counts[tag.ID],
		})
	}
	sort.SliceStable(result, func(i, j int) bool {
		left, right := result[i], result[j]
		if (left.Category == "") != (right.Category == "") {
			return right.Category == ""
		}
		if left.Category != right.Category {
			return left.Category < right.Category
		}
		if left.Count != right.Count {
			return left.Count > right.Count
		}
		return left.Name < right.Name
	})
	return result, nil
}

func (a *App) tagUsageCounts() (map[string]int, error) {
	type tagCountRow struct {
		TagID string
		Total int
	}
	var rows []tagCountRow
	if err := a.db.Model(&model.MediaTag{}).
		Select("tag_id, COUNT(*) AS total").
		Group("tag_id").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	counts := make(map[string]int, len(rows))
	for _, row := range rows {
		counts[row.TagID] = row.Total
	}
	return counts, nil
}

// CountTaggedMedia 有过标签的片子总数（去重），标签管理页顶栏的「覆盖 N 部」用。
func (a *App) CountTaggedMedia() (int, error) {
	var total int64
	err := a.db.Model(&model.MediaTag{}).Distinct("media_id").Count(&total).Error
	return int(total), err
}

// CreateMyTag 新建标签，同名（去空白后）直接返回已有的那个。
func (a *App) CreateMyTag(name, category string) (*model.Tag, error) {
	name = strings.TrimSpace(name)
	category = strings.TrimSpace(category)
	if name == "" {
		return nil, errors.New("tag name is required")
	}

	var existing model.Tag
	err := a.db.Where("name = ?", name).First(&existing).Error
	if err == nil {
		return &existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	tag := model.Tag{Name: name, Category: category, CreatedBy: desktopUserID}
	if err := a.db.Create(&tag).Error; err != nil {
		return nil, err
	}
	return &tag, nil
}

// SetMediaTags 全量覆盖一部片子的标签，按差集增删并维护 UsageCount。
func (a *App) SetMediaTags(mediaID string, tagIDs []string) error {
	mediaID = strings.TrimSpace(mediaID)
	if mediaID == "" {
		return errors.New("media id is required")
	}

	wanted := make(map[string]bool, len(tagIDs))
	for _, tagID := range tagIDs {
		if tagID = strings.TrimSpace(tagID); tagID != "" {
			wanted[tagID] = true
		}
	}

	var current []model.MediaTag
	if err := a.db.Where("media_id = ?", mediaID).Find(&current).Error; err != nil {
		return err
	}
	existing := make(map[string]bool, len(current))
	for _, link := range current {
		existing[link.TagID] = true
	}

	for tagID := range wanted {
		if existing[tagID] {
			continue
		}
		if err := a.repos.MediaTag.Create(&model.MediaTag{
			MediaID:   mediaID,
			TagID:     tagID,
			CreatedBy: desktopUserID,
		}); err != nil {
			return err
		}
		_ = a.repos.Tag.IncrementUsage(tagID)
	}
	for tagID := range existing {
		if wanted[tagID] {
			continue
		}
		if err := a.repos.MediaTag.Delete(mediaID, tagID); err != nil {
			return err
		}
		_ = a.repos.Tag.DecrementUsage(tagID)
	}

	a.broadcastMediaTagChange(mediaID)
	return nil
}

// BatchAddTag 给多部片子打同一个标签，已有的跳过。
func (a *App) BatchAddTag(mediaIDs []string, tagID string) error {
	tagID = strings.TrimSpace(tagID)
	if tagID == "" {
		return errors.New("tag id is required")
	}
	for _, mediaID := range mediaIDs {
		mediaID = strings.TrimSpace(mediaID)
		if mediaID == "" || a.repos.MediaTag.Exists(mediaID, tagID) {
			continue
		}
		if err := a.repos.MediaTag.Create(&model.MediaTag{
			MediaID:   mediaID,
			TagID:     tagID,
			CreatedBy: desktopUserID,
		}); err != nil {
			return err
		}
		_ = a.repos.Tag.IncrementUsage(tagID)
		a.broadcastMediaTagChange(mediaID)
	}
	return nil
}

// BatchRemoveTag 从多部片子上摘掉同一个标签。
func (a *App) BatchRemoveTag(mediaIDs []string, tagID string) error {
	tagID = strings.TrimSpace(tagID)
	if tagID == "" {
		return errors.New("tag id is required")
	}
	for _, mediaID := range mediaIDs {
		mediaID = strings.TrimSpace(mediaID)
		if mediaID == "" || !a.repos.MediaTag.Exists(mediaID, tagID) {
			continue
		}
		if err := a.repos.MediaTag.Delete(mediaID, tagID); err != nil {
			return err
		}
		_ = a.repos.Tag.DecrementUsage(tagID)
		a.broadcastMediaTagChange(mediaID)
	}
	return nil
}

// RenameMyTag 重命名标签。
func (a *App) RenameMyTag(tagID, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("tag name is required")
	}
	tag, err := a.repos.Tag.FindByID(strings.TrimSpace(tagID))
	if err != nil {
		return err
	}
	var clash model.Tag
	if err := a.db.Where("name = ? AND id != ?", name, tag.ID).First(&clash).Error; err == nil {
		return errors.New("标签「" + name + "」已存在")
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	tag.Name = name
	return a.repos.Tag.Update(tag)
}

// MoveMyTag 把标签挪到另一个分组，category 为空即「未分组」。
func (a *App) MoveMyTag(tagID, category string) error {
	tag, err := a.repos.Tag.FindByID(strings.TrimSpace(tagID))
	if err != nil {
		return err
	}
	tag.Category = strings.TrimSpace(category)
	return a.repos.Tag.Update(tag)
}

// MergeMyTags 把 fromTag 的关联迁到 intoTag（去重），删掉源标签，重算两边的 UsageCount。
func (a *App) MergeMyTags(fromTagID, intoTagID string) error {
	fromTagID = strings.TrimSpace(fromTagID)
	intoTagID = strings.TrimSpace(intoTagID)
	if fromTagID == "" || intoTagID == "" {
		return errors.New("tag id is required")
	}
	if fromTagID == intoTagID {
		return errors.New("cannot merge a tag into itself")
	}
	if _, err := a.repos.Tag.FindByID(intoTagID); err != nil {
		return err
	}

	var sourceLinks []model.MediaTag
	if err := a.db.Where("tag_id = ?", fromTagID).Find(&sourceLinks).Error; err != nil {
		return err
	}
	touched := make([]string, 0, len(sourceLinks))
	for _, link := range sourceLinks {
		if !a.repos.MediaTag.Exists(link.MediaID, intoTagID) {
			if err := a.repos.MediaTag.Create(&model.MediaTag{
				MediaID:   link.MediaID,
				TagID:     intoTagID,
				CreatedBy: desktopUserID,
			}); err != nil {
				return err
			}
		}
		touched = append(touched, link.MediaID)
	}

	// TagRepo.Delete 连带删掉源标签的全部关联
	if err := a.repos.Tag.Delete(fromTagID); err != nil {
		return err
	}
	if err := a.recountTagUsage(intoTagID); err != nil {
		return err
	}
	for _, mediaID := range touched {
		a.broadcastMediaTagChange(mediaID)
	}
	return nil
}

// DeleteMyTag 删除标签，关联由 TagRepo.Delete 连带清掉。
func (a *App) DeleteMyTag(tagID string) error {
	tagID = strings.TrimSpace(tagID)
	if tagID == "" {
		return errors.New("tag id is required")
	}
	var links []model.MediaTag
	if err := a.db.Where("tag_id = ?", tagID).Find(&links).Error; err != nil {
		return err
	}
	if err := a.repos.Tag.Delete(tagID); err != nil {
		return err
	}
	for _, link := range links {
		a.broadcastMediaTagChange(link.MediaID)
	}
	return nil
}

func (a *App) recountTagUsage(tagID string) error {
	var total int64
	if err := a.db.Model(&model.MediaTag{}).Where("tag_id = ?", tagID).Count(&total).Error; err != nil {
		return err
	}
	return a.db.Model(&model.Tag{}).Where("id = ?", tagID).
		UpdateColumn("usage_count", total).Error
}

func (a *App) broadcastMediaTagChange(mediaID string) {
	_, tagSet := a.loadMediaUserSets([]string{mediaID})
	tags := emptyTagsIfNil(tagSet[mediaID])
	a.broadcastMediaUserState(mediaID, nil, &tags)
}

// ==================== 组合筛选 ====================

// applyUserFilter 叠加评分和标签条件。
// 同一组内多选 = OR（一条 IN 子查询），不同组之间 = AND（多条子查询）。
func (a *App) applyUserFilter(query *gorm.DB, uf UserFilter) *gorm.DB {
	// 评分档也是一个「组」：多选之间是或，和标签组之间才是且
	rated, includeUnrated := splitScores(uf.Scores)
	anyRated := a.db.Table("media_ratings").Select("media_id").Where("user_id = ?", desktopUserID)
	switch {
	case len(rated) > 0 && includeUnrated:
		scoreMatch := a.db.Table("media_ratings").Select("media_id").
			Where("user_id = ? AND score IN ?", desktopUserID, rated)
		query = query.Where(
			a.db.Where("media.id IN (?)", scoreMatch).Or("media.id NOT IN (?)", anyRated),
		)
	case len(rated) > 0:
		scoreMatch := a.db.Table("media_ratings").Select("media_id").
			Where("user_id = ? AND score IN ?", desktopUserID, rated)
		query = query.Where("media.id IN (?)", scoreMatch)
	case includeUnrated:
		query = query.Where("media.id NOT IN (?)", anyRated)
	}

	for _, group := range uf.TagGroups {
		group = nonEmptyStrings(group)
		if len(group) == 0 {
			continue
		}
		sub := a.db.Table("media_tags").Select("media_id").Where("tag_id IN ?", group)
		query = query.Where("media.id IN (?)", sub)
	}
	return query
}

func nonEmptyStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

// FilterFacets 筛选面板上每个选项还能筛出几部。
type FilterFacets struct {
	Tags    map[string]int `json:"tags"`    // tag_id -> 数量
	Ratings map[string]int `json:"ratings"` // "1".."5" 和 "unrated" -> 数量
	Total   int            `json:"total"`   // 当前全部条件下的总数
}

// GetFilterFacets 计算筛选面板的计数。
// 标准 facet 语义：算某一组内选项的数量时，排除该组自身的条件，只套其他组，
// 否则选中「70-80分」以后同组的「80-90分」会显示 0，用户就没法再多选一个档。
func (a *App) GetFilterFacets(libraryID, keyword, filterType, filterValue string, uf UserFilter) (FilterFacets, error) {
	facets := FilterFacets{
		Tags:    map[string]int{},
		Ratings: map[string]int{},
	}
	if a.db == nil {
		return facets, errors.New("application database is not initialized")
	}

	groups := make([][]string, 0, len(uf.TagGroups))
	for _, group := range uf.TagGroups {
		if group = nonEmptyStrings(group); len(group) > 0 {
			groups = append(groups, group)
		}
	}

	// 总数：全部条件都套上
	total, err := a.countUserFiltered(libraryID, keyword, filterType, filterValue, uf)
	if err != nil {
		return facets, err
	}
	facets.Total = int(total)

	// 评分档：排除评分自身的条件，保留全部标签组
	ratingScope := UserFilter{TagGroups: groups}
	ratingTotal, err := a.countUserFiltered(libraryID, keyword, filterType, filterValue, ratingScope)
	if err != nil {
		return facets, err
	}
	if ratingTotal > 0 {
		type scoreRow struct {
			Score int
			Total int
		}
		var rows []scoreRow
		if err := a.db.Model(&model.MediaRating{}).
			Select("score, COUNT(*) AS total").
			Where("user_id = ? AND media_id IN (?)", desktopUserID,
				a.facetMediaIDSubQuery(libraryID, keyword, filterType, filterValue, ratingScope)).
			Group("score").Scan(&rows).Error; err != nil {
			return facets, err
		}
		rated := 0
		for _, row := range rows {
			if row.Score >= 1 && row.Score <= 5 {
				facets.Ratings[strconv.Itoa(row.Score)] = row.Total
				rated += row.Total
			}
		}
		facets.Ratings["unrated"] = int(ratingTotal) - rated
	}
	for score := 1; score <= 5; score++ {
		key := strconv.Itoa(score)
		if _, ok := facets.Ratings[key]; !ok {
			facets.Ratings[key] = 0
		}
	}
	if _, ok := facets.Ratings["unrated"]; !ok {
		facets.Ratings["unrated"] = 0
	}

	// 组只带着「已选中」的 tag_id，但同组里没选中的那些也要按排除自身来算，
	// 否则选了「70-80分」以后同组的「80-90分」会显示 0，用户就没法再多选一档。
	var allTags []model.Tag
	if err := a.db.Model(&model.Tag{}).Find(&allTags).Error; err != nil {
		return facets, err
	}
	categoryByTag := make(map[string]string, len(allTags))
	for _, tag := range allTags {
		categoryByTag[tag.ID] = tag.Category
	}

	// 标签：每个组各算一次，算的时候排除该组自身
	for index, group := range groups {
		categories := make(map[string]bool, len(group))
		for _, tagID := range group {
			categories[categoryByTag[tagID]] = true
		}
		covered := make([]string, 0, len(allTags))
		for _, tag := range allTags {
			if categories[tag.Category] {
				covered = append(covered, tag.ID)
			}
		}
		if err := a.countTagFacets(libraryID, keyword, filterType, filterValue, uf, groups, index, covered, facets.Tags); err != nil {
			return facets, err
		}
	}
	// 剩下的标签：全部条件都套上再数
	allTagIDs := make([]string, 0, len(allTags))
	for _, tag := range allTags {
		allTagIDs = append(allTagIDs, tag.ID)
	}
	if err := a.countTagFacets(libraryID, keyword, filterType, filterValue, uf, groups, -1, allTagIDs, facets.Tags); err != nil {
		return facets, err
	}

	return facets, nil
}

// countTagFacets 数一遍 tag_id 的分布，把结果写进 out 里 covered 指定的那些标签。
// excludeGroup >= 0 时跳过该组自身的条件；先算各组、再算全条件那轮，
// 全条件那轮只补没被写过的键，所以顺序不能倒过来。
func (a *App) countTagFacets(
	libraryID, keyword, filterType, filterValue string,
	uf UserFilter, groups [][]string, excludeGroup int, covered []string, out map[string]int,
) error {
	scoped := UserFilter{Scores: uf.Scores}
	for index, group := range groups {
		if index == excludeGroup {
			continue
		}
		scoped.TagGroups = append(scoped.TagGroups, group)
	}

	type tagRow struct {
		TagID string
		Total int
	}
	var rows []tagRow
	if err := a.db.Model(&model.MediaTag{}).
		Select("tag_id, COUNT(*) AS total").
		Where("media_id IN (?)", a.facetMediaIDSubQuery(libraryID, keyword, filterType, filterValue, scoped)).
		Group("tag_id").Scan(&rows).Error; err != nil {
		return err
	}
	counts := make(map[string]int, len(rows))
	for _, row := range rows {
		counts[row.TagID] = row.Total
	}

	for _, tagID := range covered {
		if _, taken := out[tagID]; taken {
			continue
		}
		// 没被数到的一律 0，前端才能置灰
		out[tagID] = counts[tagID]
	}
	return nil
}

func (a *App) countUserFiltered(
	libraryID, keyword, filterType, filterValue string, uf UserFilter,
) (int64, error) {
	query := a.buildMediaQuery(libraryID, "", keyword, filterType, filterValue)
	query = a.applyUserFilter(query, uf)
	var total int64
	err := query.Count(&total).Error
	return total, err
}

// facetMediaIDSubQuery 返回「符合这套条件的 media.id」的子查询。
// 一定要留在 SQL 里：早先的写法是把 id 全捞回 Go 再当成 IN 的绑定参数塞回去，
// 库一大就撞上 SQLite 的变量上限（实测 4 万部直接报 too many SQL variables），
// 而且白白把几万个 id 搬进搬出。
func (a *App) facetMediaIDSubQuery(libraryID, keyword, filterType, filterValue string, uf UserFilter) *gorm.DB {
	query := a.buildMediaQuery(libraryID, "", keyword, filterType, filterValue)
	query = a.applyUserFilter(query, uf)
	return query.Select("media.id")
}
