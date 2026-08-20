package service

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"go.uber.org/zap"
	"gorm.io/gorm"
	"navi-desktop/model"
	"navi-desktop/repository"
)

const (
	RecommendationVersionSettingKey       = "recommendation.version"
	DetailRecommendationSchemaVersion     = 1
	DetailRecommendationFeatureVersion    = 3
	detailRecommendationCacheTTLHours     = 24 * 30
	detailRecommendationMinVisibleCount   = 1
	detailRecommendationDefaultLimit      = 12
	detailRecommendationCachedPoolMaxSize = 60
	recommendationUserID                  = "desktop_user"
)

type RelatedMediaItem struct {
	Media         model.Media `json:"media"`
	Reason        string      `json:"reason"`
	MatchType     string      `json:"match_type"`
	Score         float64     `json:"score"`
	MatchedValues []string    `json:"matched_values,omitempty"`
}

type DetailRecommendationResponse struct {
	ContinueWatching []RelatedMediaItem `json:"continue_watching"`
	MoreLikeThis     []RelatedMediaItem `json:"more_like_this"`
}

type normalizedGenres struct {
	ThemeTags     []string `json:"theme_tags"`
	CoreThemeTags []string `json:"core_theme_tags"`
	VendorTags    []string `json:"vendor_tags"`
	TechTags      []string `json:"tech_tags"`
}

type DetailRecommendationService struct {
	repos *repository.Repositories
	log   *zap.SugaredLogger

	normalizedGenresMu sync.RWMutex
	normalizedGenres   map[string]normalizedGenres
}

type recommendationCachePayload struct {
	Candidates []RelatedMediaItem `json:"candidates"`
}

type recommendationSeed struct {
	Media  model.Media
	Routes map[string]bool
}

type actorRef struct {
	ID   string
	Name string
}

type vendorMatch struct {
	Kind  string
	Value string
}

type selectionState struct {
	selectedIDs    map[string]bool
	actorCounts    map[string]int
	vendorCounts   map[string]int
	prefixCounts   map[string]int
	favoriteSet    map[string]bool
	watchedSet     map[string]bool
	ratingSet      map[string]int
	remainingLimit int
}

type routePlan struct {
	MatchType string
	Limit     int
}

var continueRoutePlan = []routePlan{
	{MatchType: "series", Limit: 2},
	{MatchType: "actor", Limit: 3},
	{MatchType: "vendor", Limit: 2},
}

var moreLikeRoutePlan = []routePlan{
	{MatchType: "tag", Limit: 3},
	{MatchType: "prefix", Limit: 1},
	{MatchType: "explore", Limit: 1},
}

var technicalTagTokens = map[string]bool{
	"4K": true, "8K": true, "UHD": true, "FHD": true, "HD": true, "SD": true,
	"H264": true, "H265": true, "HEVC": true, "AV1": true, "HDR": true,
	"60FPS": true, "FPS": true, "WEB-DL": true, "REMUX": true, "X264": true, "X265": true,
	"1080P": true, "720P": true, "2160P": true, "480P": true,
	"中文字幕": true, "字幕": true, "无码": true, "有码": true,
}

var allowedShortThemeTokens = map[string]bool{
	"VR": true,
	"3D": true,
	"OL": true,
}

var genericThemeTokens = map[string]bool{
	"剧情":   true,
	"劇情":   true,
	"单体":   true,
	"單體":   true,
	"单体作品": true,
	"單體作品": true,
	"作品":   true,
	"系列":   true,
	"合集":   true,
	"综合":   true,
	"綜合":   true,
	"企划":   true,
	"企劃":   true,
	"配信":   true,
	"独占":   true,
	"獨占":   true,
	"其他":   true,
}

func NewDetailRecommendationService(repos *repository.Repositories, log *zap.SugaredLogger) *DetailRecommendationService {
	return &DetailRecommendationService{
		repos:            repos,
		log:              log,
		normalizedGenres: make(map[string]normalizedGenres),
	}
}

func (s *DetailRecommendationService) ClearNormalizedGenres() {
	s.normalizedGenresMu.Lock()
	defer s.normalizedGenresMu.Unlock()
	s.normalizedGenres = make(map[string]normalizedGenres)
}

func (s *DetailRecommendationService) InvalidateNormalizedGenres(mediaID string) {
	mediaID = strings.TrimSpace(mediaID)
	if mediaID == "" {
		return
	}
	s.normalizedGenresMu.Lock()
	defer s.normalizedGenresMu.Unlock()
	delete(s.normalizedGenres, mediaID)
}

func (s *DetailRecommendationService) GetDetailRecommendations(mediaID string, limit int) (*DetailRecommendationResponse, error) {
	source, err := s.repos.Media.FindByID(mediaID)
	if err != nil {
		return nil, err
	}
	ApplyDerivedMediaFields(source)

	if limit <= 0 {
		limit = detailRecommendationDefaultLimit
	}

	cacheKey := s.buildCacheKey(mediaID)
	var payload recommendationCachePayload
	if cached, ok := s.repos.AICache.Get(cacheKey); ok {
		if err := json.Unmarshal([]byte(cached), &payload); err != nil {
			s.log.Warnf("unmarshal recommendation cache failed: %v", err)
		}
	}

	if len(payload.Candidates) == 0 {
		payload, err = s.buildCachePayload(source)
		if err != nil {
			return nil, err
		}
		if encoded, err := json.Marshal(payload); err == nil {
			if err := s.repos.AICache.Set(cacheKey, string(encoded), detailRecommendationCacheTTLHours); err != nil {
				s.log.Warnf("set recommendation cache failed: %v", err)
			}
		}
	}

	return s.buildResponseForUser(source, payload.Candidates, limit)
}

func (s *DetailRecommendationService) buildCacheKey(mediaID string) string {
	recoVersion := s.getRecommendationVersion()
	return fmt.Sprintf(
		"detail_reco:%s:sv%d:fv%d:rv%d",
		strings.TrimSpace(mediaID),
		DetailRecommendationSchemaVersion,
		DetailRecommendationFeatureVersion,
		recoVersion,
	)
}

func (s *DetailRecommendationService) getRecommendationVersion() int {
	raw, err := s.repos.SystemSetting.Get(RecommendationVersionSettingKey)
	if err != nil {
		return 1
	}
	return parseIntSetting(raw, 1)
}

func (s *DetailRecommendationService) buildCachePayload(source *model.Media) (recommendationCachePayload, error) {
	sourceActors, err := s.loadActorRefs([]string{source.ID})
	if err != nil {
		return recommendationCachePayload{}, err
	}
	sourceActorRefs := sourceActors[source.ID]
	sourceGenres := s.getNormalizedGenres(source, sourceActorRefs)

	seeds := make(map[string]*recommendationSeed)
	addSeed := func(route string, items []model.Media) {
		for _, item := range items {
			if item.ID == "" {
				continue
			}
			seed, ok := seeds[item.ID]
			if !ok {
				seed = &recommendationSeed{
					Media:  item,
					Routes: make(map[string]bool),
				}
				seeds[item.ID] = seed
			}
			seed.Routes[route] = true
		}
	}

	addSeed("series", s.querySeriesCandidates(source, 24))
	addSeed("actor", s.queryActorCandidates(source, actorIDs(sourceActorRefs), 40))
	addSeed("vendor", s.queryVendorCandidates(source, 32))
	addSeed("tag", s.queryTagCandidates(source, sourceGenres.CoreThemeTags, 64))
	addSeed("prefix", s.queryPrefixCandidates(source, 20))
	recent, favoriteExplore := s.queryExploreCandidates(source, 32)
	addSeed("recent", recent)
	addSeed("favorite", favoriteExplore)

	candidateIDs := make([]string, 0, len(seeds))
	for mediaID := range seeds {
		candidateIDs = append(candidateIDs, mediaID)
	}

	actorMap, err := s.loadActorRefs(candidateIDs)
	if err != nil {
		return recommendationCachePayload{}, err
	}

	candidates := make([]RelatedMediaItem, 0, len(seeds))
	for _, seed := range seeds {
		ApplyDerivedMediaFields(&seed.Media)
		if !isStaticRecommendationCandidate(source, &seed.Media) {
			continue
		}

		item, ok := s.buildCandidateItem(source, sourceActorRefs, sourceGenres, seed, actorMap[seed.Media.ID])
		if !ok {
			continue
		}
		candidates = append(candidates, item)
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Score == candidates[j].Score {
			return candidates[i].Media.CreatedAt.After(candidates[j].Media.CreatedAt)
		}
		return candidates[i].Score > candidates[j].Score
	})

	if len(candidates) > detailRecommendationCachedPoolMaxSize {
		candidates = candidates[:detailRecommendationCachedPoolMaxSize]
	}

	return recommendationCachePayload{Candidates: candidates}, nil
}

func (s *DetailRecommendationService) buildCandidateItem(
	source *model.Media,
	sourceActors []actorRef,
	sourceGenres normalizedGenres,
	seed *recommendationSeed,
	candidateActors []actorRef,
) (RelatedMediaItem, bool) {
	candidateGenres := s.getNormalizedGenres(&seed.Media, candidateActors)

	sameSeries := source.SeriesID != "" && seed.Media.SeriesID != "" && source.SeriesID == seed.Media.SeriesID
	actorMatches := intersectActorNames(sourceActors, candidateActors)
	vendorMatches := findVendorMatches(source, &seed.Media)
	tagMatches := intersectStrings(sourceGenres.CoreThemeTags, candidateGenres.CoreThemeTags)
	samePrefix := source.CodePrefix != "" && seed.Media.CodePrefix != "" && source.CodePrefix == seed.Media.CodePrefix

	matchType := ""
	reason := ""
	matchedValues := []string(nil)
	score := 0.0

	switch {
	case sameSeries:
		matchType = "series"
		reason = "同系列"
		score += 40
	case len(actorMatches) > 0:
		matchType = "actor"
		matchedValues = actorMatches
		reason = "同演员：" + joinDisplayValues(actorMatches, 2)
		score += 24 + float64(len(actorMatches))*18
	case len(vendorMatches) > 0:
		matchType = "vendor"
		reason, matchedValues = buildVendorReason(vendorMatches)
		score += 16 + float64(len(vendorMatches))*10
	case len(tagMatches) > 0:
		matchType = "tag"
		matchedValues = tagMatches
		reason = "相似标签：" + joinDisplayValues(tagMatches, 2)
		score += 12 + float64(len(tagMatches))*10
	case samePrefix:
		matchType = "prefix"
		matchedValues = []string{source.CodePrefix}
		reason = "同编号前缀：" + source.CodePrefix
		score += 10
	case seed.Routes["favorite"] || seed.Routes["recent"]:
		matchType = "explore"
		reason = buildExploreReason(seed, &seed.Media)
		if seed.Routes["favorite"] {
			score += 6
			matchedValues = []string{"favorite"}
		}
		if seed.Routes["recent"] {
			score += 4
		}
	default:
		return RelatedMediaItem{}, false
	}

	score += yearClosenessScore(source.Year, seed.Media.Year)
	score += freshnessScore(seed.Media.CreatedAt)
	score += 4 * scoreFromMetadata(seed.Media.MetadataScore, 100)
	score += favoriteBoostScore(seed)
	score += ratingWeakScore(seed.Media.Rating)

	if score <= 0 {
		return RelatedMediaItem{}, false
	}

	return RelatedMediaItem{
		Media:         seed.Media,
		Reason:        reason,
		MatchType:     matchType,
		Score:         score,
		MatchedValues: matchedValues,
	}, true
}

func (s *DetailRecommendationService) buildResponseForUser(source *model.Media, candidates []RelatedMediaItem, limit int) (*DetailRecommendationResponse, error) {
	if limit <= 0 {
		limit = detailRecommendationDefaultLimit
	}

	mediaIDs := make([]string, 0, len(candidates))
	for _, item := range candidates {
		if item.Media.ID != "" {
			mediaIDs = append(mediaIDs, item.Media.ID)
		}
	}

	favoriteSet, watchedSet := s.loadMediaStateSets(mediaIDs)
	ratingSet := s.loadMyRatingSet(mediaIDs)
	state := &selectionState{
		selectedIDs:    make(map[string]bool),
		actorCounts:    make(map[string]int),
		vendorCounts:   make(map[string]int),
		prefixCounts:   make(map[string]int),
		favoriteSet:    favoriteSet,
		watchedSet:     watchedSet,
		ratingSet:      ratingSet,
		remainingLimit: limit,
	}

	byType := make(map[string][]RelatedMediaItem)
	for _, item := range candidates {
		byType[item.MatchType] = append(byType[item.MatchType], item)
	}

	continueWatching := s.selectByRoutePlan(source, byType, continueRoutePlan, state)
	moreLikeThis := s.selectByRoutePlan(source, byType, moreLikeRoutePlan, state)

	if state.remainingLimit > 0 {
		moreLikeThis = s.refillSectionByPriority(
			source,
			byType,
			[]string{"tag"},
			state,
			moreLikeThis,
			state.remainingLimit,
		)
	}
	if state.remainingLimit > 0 {
		continueWatching = s.refillSectionByPriority(
			source,
			byType,
			[]string{"series", "actor", "vendor"},
			state,
			continueWatching,
			state.remainingLimit,
		)
	}
	if state.remainingLimit > 0 {
		moreLikeThis = s.refillSectionByPriority(
			source,
			byType,
			[]string{"prefix", "explore"},
			state,
			moreLikeThis,
			state.remainingLimit,
		)
	}

	total := len(continueWatching) + len(moreLikeThis)
	if total < detailRecommendationMinVisibleCount {
		return &DetailRecommendationResponse{}, nil
	}

	return &DetailRecommendationResponse{
		ContinueWatching: continueWatching,
		MoreLikeThis:     moreLikeThis,
	}, nil
}

func (s *DetailRecommendationService) selectByRoutePlan(
	source *model.Media,
	itemsByType map[string][]RelatedMediaItem,
	plan []routePlan,
	state *selectionState,
) []RelatedMediaItem {
	selected := make([]RelatedMediaItem, 0)
	if state == nil || state.remainingLimit <= 0 {
		return selected
	}

	sectionTarget := routePlanTotalLimit(plan)
	if sectionTarget > state.remainingLimit {
		sectionTarget = state.remainingLimit
	}
	if sectionTarget <= 0 {
		return selected
	}

	// First pass: honor each route's base quota.
	for _, route := range plan {
		if state.remainingLimit <= 0 || len(selected) >= sectionTarget {
			break
		}

		limit := route.Limit
		if limit > sectionTarget-len(selected) {
			limit = sectionTarget - len(selected)
		}
		if limit <= 0 {
			continue
		}

		items := itemsByType[route.MatchType]
		picked := 0
		for _, item := range items {
			if picked >= limit || state.remainingLimit <= 0 || len(selected) >= sectionTarget {
				break
			}
			if selectRecommendationItem(source, item, state, &selected) {
				picked++
			}
		}
	}

	// Second pass: refill the section from the same route pool in priority order
	// until the section target is met or candidates are exhausted.
	if len(selected) < sectionTarget && state.remainingLimit > 0 {
		for _, route := range plan {
			if state.remainingLimit <= 0 || len(selected) >= sectionTarget {
				break
			}

			items := itemsByType[route.MatchType]
			for _, item := range items {
				if state.remainingLimit <= 0 || len(selected) >= sectionTarget {
					break
				}
				selectRecommendationItem(source, item, state, &selected)
			}
		}
	}

	return selected
}

func (s *DetailRecommendationService) refillSectionByPriority(
	source *model.Media,
	itemsByType map[string][]RelatedMediaItem,
	priorities []string,
	state *selectionState,
	selected []RelatedMediaItem,
	need int,
) []RelatedMediaItem {
	if state == nil || need <= 0 || len(priorities) == 0 {
		return selected
	}

	remaining := need
	for _, matchType := range priorities {
		if remaining <= 0 || state.remainingLimit <= 0 {
			break
		}

		items := itemsByType[matchType]
		for _, item := range items {
			if remaining <= 0 || state.remainingLimit <= 0 {
				break
			}
			if selectRecommendationItem(source, item, state, &selected) {
				remaining--
			}
		}
	}

	return selected
}

func selectRecommendationItem(source *model.Media, item RelatedMediaItem, state *selectionState, selected *[]RelatedMediaItem) bool {
	if state == nil || selected == nil {
		return false
	}
	if !canSelectRecommendation(source, item, state) {
		return false
	}

	item.Media.IsFavorite = state.favoriteSet[item.Media.ID]
	item.Media.MyRating = state.ratingSet[item.Media.ID]
	item.Media.IsWatched = false

	registerSelection(item, state)
	*selected = append(*selected, item)
	state.remainingLimit--
	return true
}

func routePlanTotalLimit(plan []routePlan) int {
	total := 0
	for _, route := range plan {
		if route.Limit > 0 {
			total += route.Limit
		}
	}
	return total
}

func canSelectRecommendation(source *model.Media, item RelatedMediaItem, state *selectionState) bool {
	if item.Media.ID == "" || state.selectedIDs[item.Media.ID] {
		return false
	}
	if state.watchedSet[item.Media.ID] {
		return false
	}
	if !isStaticRecommendationCandidate(source, &item.Media) {
		return false
	}

	switch item.MatchType {
	case "actor":
		for _, value := range item.MatchedValues {
			key := normalizeComparableValue(value)
			if key == "" {
				continue
			}
			if state.actorCounts[key] >= 2 {
				return false
			}
			return true
		}
	case "vendor":
		for _, value := range item.MatchedValues {
			key := normalizeComparableValue(value)
			if key == "" {
				continue
			}
			if state.vendorCounts[key] >= 2 {
				return false
			}
			return true
		}
	case "prefix":
		key := normalizeComparableValue(item.Media.CodePrefix)
		if key == "" && len(item.MatchedValues) > 0 {
			key = normalizeComparableValue(item.MatchedValues[0])
		}
		return state.prefixCounts[key] < 2
	}

	return true
}

func registerSelection(item RelatedMediaItem, state *selectionState) {
	state.selectedIDs[item.Media.ID] = true

	switch item.MatchType {
	case "actor":
		for _, value := range item.MatchedValues {
			key := normalizeComparableValue(value)
			if key == "" {
				continue
			}
			state.actorCounts[key]++
			return
		}
	case "vendor":
		for _, value := range item.MatchedValues {
			key := normalizeComparableValue(value)
			if key == "" {
				continue
			}
			state.vendorCounts[key]++
			return
		}
	case "prefix":
		key := normalizeComparableValue(item.Media.CodePrefix)
		if key == "" && len(item.MatchedValues) > 0 {
			key = normalizeComparableValue(item.MatchedValues[0])
		}
		if key != "" {
			state.prefixCounts[key]++
		}
	}
}

func (s *DetailRecommendationService) loadMediaStateSets(mediaIDs []string) (map[string]bool, map[string]bool) {
	favoriteSet := make(map[string]bool, len(mediaIDs))
	watchedSet := make(map[string]bool, len(mediaIDs))
	if len(mediaIDs) == 0 {
		return favoriteSet, watchedSet
	}

	var favoriteIDs []string
	if err := s.repos.DB().Model(&model.Favorite{}).
		Where("user_id = ? AND media_id IN ?", recommendationUserID, mediaIDs).
		Pluck("media_id", &favoriteIDs).Error; err == nil {
		for _, mediaID := range favoriteIDs {
			favoriteSet[mediaID] = true
		}
	}

	var watchedIDs []string
	if err := s.repos.DB().Model(&model.WatchHistory{}).
		Where("user_id = ? AND completed = ? AND media_id IN ?", recommendationUserID, true, mediaIDs).
		Pluck("media_id", &watchedIDs).Error; err == nil {
		for _, mediaID := range watchedIDs {
			watchedSet[mediaID] = true
		}
	}

	return favoriteSet, watchedSet
}

func (s *DetailRecommendationService) loadMyRatingSet(mediaIDs []string) map[string]int {
	ratingSet := make(map[string]int, len(mediaIDs))
	if len(mediaIDs) == 0 {
		return ratingSet
	}

	var ratings []model.MediaRating
	if err := s.repos.DB().Model(&model.MediaRating{}).
		Where("user_id = ? AND media_id IN ?", recommendationUserID, mediaIDs).
		Find(&ratings).Error; err == nil {
		for _, rating := range ratings {
			if rating.Score > 0 {
				ratingSet[rating.MediaID] = rating.Score
			}
		}
	}

	return ratingSet
}

func (s *DetailRecommendationService) loadActorRefs(mediaIDs []string) (map[string][]actorRef, error) {
	result := make(map[string][]actorRef)
	if len(mediaIDs) == 0 {
		return result, nil
	}

	type row struct {
		MediaID   string
		PersonID  string
		Name      string
		SortOrder int
	}
	var rows []row
	err := s.repos.DB().
		Table("media_people").
		Select("media_people.media_id AS media_id, media_people.person_id AS person_id, people.name AS name, media_people.sort_order AS sort_order").
		Joins("JOIN people ON people.id = media_people.person_id").
		Where("media_people.role = ? AND media_people.media_id IN ?", "actor", mediaIDs).
		Order("media_people.sort_order ASC, people.name ASC").
		Scan(&rows).Error
	if err != nil {
		return result, err
	}

	for _, row := range rows {
		name := strings.TrimSpace(row.Name)
		if row.MediaID == "" || name == "" {
			continue
		}
		result[row.MediaID] = append(result[row.MediaID], actorRef{
			ID:   strings.TrimSpace(row.PersonID),
			Name: name,
		})
	}
	return result, nil
}

func (s *DetailRecommendationService) getNormalizedGenres(media *model.Media, actors []actorRef) normalizedGenres {
	if media == nil || media.ID == "" {
		return normalizedGenres{}
	}

	s.normalizedGenresMu.RLock()
	cached, ok := s.normalizedGenres[media.ID]
	s.normalizedGenresMu.RUnlock()
	if ok {
		return cached
	}

	actorKeys := make(map[string]bool)
	for _, actor := range actors {
		actorKeys[normalizeComparableValue(actor.Name)] = true
	}

	var themeTags []string
	var coreThemeTags []string
	var vendorTags []string
	var techTags []string

	for _, token := range strings.Split(strings.TrimSpace(media.Genres), ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}

		key := normalizeComparableValue(token)
		upper := strings.ToUpper(strings.TrimSpace(token))
		if key == "" {
			continue
		}
		if actorKeys[key] {
			continue
		}

		switch {
		case strings.Contains(token, ":") || strings.Contains(token, "："):
			vendorTags = append(vendorTags, strings.TrimSpace(stripTagPrefix(token)))
		case technicalTagTokens[upper]:
			techTags = append(techTags, token)
		case ParseCodePrefix(token) != "" && strings.Contains(token, "-"):
			techTags = append(techTags, token)
		case len(upper) <= 3 && !allowedShortThemeTokens[upper]:
			techTags = append(techTags, token)
		case looksLikeIdentifierTag(token, upper):
			continue
		default:
			themeTags = append(themeTags, token)
			if isCoreContentTag(token, upper) {
				coreThemeTags = append(coreThemeTags, token)
			}
		}
	}

	normalized := normalizedGenres{
		ThemeTags:     dedupeCaseInsensitive(themeTags),
		CoreThemeTags: dedupeCaseInsensitive(coreThemeTags),
		VendorTags:    dedupeCaseInsensitive(vendorTags),
		TechTags:      dedupeCaseInsensitive(techTags),
	}

	s.normalizedGenresMu.Lock()
	s.normalizedGenres[media.ID] = normalized
	s.normalizedGenresMu.Unlock()
	return normalized
}

func (s *DetailRecommendationService) querySeriesCandidates(source *model.Media, limit int) []model.Media {
	if source == nil || source.SeriesID == "" {
		return nil
	}

	var result []model.Media
	if err := s.repos.DB().
		Model(&model.Media{}).
		Where("library_id = ? AND media_type = ? AND id <> ? AND series_id = ?", source.LibraryID, source.MediaType, source.ID, source.SeriesID).
		Order("created_at DESC").
		Limit(limit).
		Find(&result).Error; err != nil {
		s.log.Warnf("query series candidates failed: %v", err)
	}
	return result
}

func (s *DetailRecommendationService) queryActorCandidates(source *model.Media, actorIDs []string, limit int) []model.Media {
	if source == nil || len(actorIDs) == 0 {
		return nil
	}

	var result []model.Media
	err := s.repos.DB().
		Model(&model.Media{}).
		Select("media.*").
		Joins("JOIN media_people ON media_people.media_id = media.id AND media_people.role = ?", "actor").
		Where("media.library_id = ? AND media.media_type = ? AND media.id <> ? AND media_people.person_id IN ?", source.LibraryID, source.MediaType, source.ID, actorIDs).
		Group("media.id").
		Order("COUNT(DISTINCT media_people.person_id) DESC, media.created_at DESC").
		Limit(limit).
		Find(&result).Error
	if err != nil {
		s.log.Warnf("query actor candidates failed: %v", err)
	}
	return result
}

func (s *DetailRecommendationService) queryVendorCandidates(source *model.Media, limit int) []model.Media {
	if source == nil {
		return nil
	}

	ApplyDerivedMediaFields(source)

	var clauses []string
	var args []interface{}

	if source.Studio != "" {
		clauses = append(clauses, "studio = ?")
		args = append(args, source.Studio)
	}
	if source.Maker != "" {
		clauses = append(clauses, "maker = ?")
		args = append(args, source.Maker)
		clauses = append(clauses, "nfo_extra_fields LIKE ?")
		args = append(args, buildJSONLikePattern("maker", source.Maker))
	}
	if source.Label != "" {
		clauses = append(clauses, "label = ?")
		args = append(args, source.Label)
		clauses = append(clauses, "nfo_extra_fields LIKE ?")
		args = append(args, buildJSONLikePattern("label", source.Label))
		clauses = append(clauses, "nfo_extra_fields LIKE ?")
		args = append(args, buildJSONLikePattern("publisher", source.Label))
	}
	if len(clauses) == 0 {
		return nil
	}

	var result []model.Media
	err := s.repos.DB().
		Model(&model.Media{}).
		Where("library_id = ? AND media_type = ? AND id <> ?", source.LibraryID, source.MediaType, source.ID).
		Where("("+strings.Join(clauses, " OR ")+")", args...).
		Order("created_at DESC").
		Limit(limit).
		Find(&result).Error
	if err != nil {
		s.log.Warnf("query vendor candidates failed: %v", err)
	}
	return result
}

func (s *DetailRecommendationService) queryTagCandidates(source *model.Media, themeTags []string, limit int) []model.Media {
	if source == nil || len(themeTags) == 0 {
		return nil
	}

	var clauses []string
	var args []interface{}
	for _, tag := range themeTags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		clauses = append(clauses, "genres LIKE ?")
		args = append(args, "%"+tag+"%")
	}
	if len(clauses) == 0 {
		return nil
	}

	var result []model.Media
	err := s.repos.DB().
		Model(&model.Media{}).
		Where("library_id = ? AND media_type = ? AND id <> ?", source.LibraryID, source.MediaType, source.ID).
		Where("("+strings.Join(clauses, " OR ")+")", args...).
		Order("created_at DESC").
		Limit(limit).
		Find(&result).Error
	if err != nil {
		s.log.Warnf("query tag candidates failed: %v", err)
	}
	return result
}

func (s *DetailRecommendationService) queryPrefixCandidates(source *model.Media, limit int) []model.Media {
	if source == nil {
		return nil
	}
	ApplyDerivedMediaFields(source)
	if source.CodePrefix == "" {
		return nil
	}

	prefixPattern := "%" + strings.ToUpper(source.CodePrefix) + "-%"

	var result []model.Media
	err := s.repos.DB().
		Model(&model.Media{}).
		Where("library_id = ? AND media_type = ? AND id <> ?", source.LibraryID, source.MediaType, source.ID).
		Where(
			"code_prefix = ? OR UPPER(file_path) LIKE ? OR UPPER(title) LIKE ? OR nfo_extra_fields LIKE ?",
			source.CodePrefix,
			prefixPattern,
			prefixPattern,
			buildCodeJSONLikePattern(source.CodePrefix),
		).
		Order("created_at DESC").
		Limit(limit).
		Find(&result).Error
	if err != nil {
		s.log.Warnf("query prefix candidates failed: %v", err)
	}
	return result
}

func (s *DetailRecommendationService) queryExploreCandidates(source *model.Media, limit int) ([]model.Media, []model.Media) {
	if source == nil {
		return nil, nil
	}

	var recent []model.Media
	if err := s.repos.DB().
		Model(&model.Media{}).
		Where("library_id = ? AND media_type = ? AND id <> ?", source.LibraryID, source.MediaType, source.ID).
		Order("created_at DESC").
		Limit(limit).
		Find(&recent).Error; err != nil {
		s.log.Warnf("query recent explore candidates failed: %v", err)
	}

	var favorites []model.Media
	if err := s.repos.DB().
		Model(&model.Media{}).
		Select("media.*").
		Joins("JOIN favorites ON favorites.media_id = media.id").
		Where("favorites.user_id = ? AND media.library_id = ? AND media.media_type = ? AND media.id <> ?", recommendationUserID, source.LibraryID, source.MediaType, source.ID).
		Order("favorites.created_at DESC").
		Limit(limit / 2).
		Find(&favorites).Error; err != nil && err != gorm.ErrRecordNotFound {
		s.log.Warnf("query favorite explore candidates failed: %v", err)
	}

	return recent, favorites
}

func actorIDs(actors []actorRef) []string {
	ids := make([]string, 0, len(actors))
	seen := make(map[string]bool)
	for _, actor := range actors {
		if actor.ID == "" || seen[actor.ID] {
			continue
		}
		seen[actor.ID] = true
		ids = append(ids, actor.ID)
	}
	return ids
}

func intersectActorNames(sourceActors []actorRef, candidateActors []actorRef) []string {
	if len(sourceActors) == 0 || len(candidateActors) == 0 {
		return nil
	}

	sourceByID := make(map[string]string)
	sourceByName := make(map[string]string)
	for _, actor := range sourceActors {
		name := strings.TrimSpace(actor.Name)
		if name == "" {
			continue
		}
		if actor.ID != "" {
			sourceByID[actor.ID] = name
		}
		sourceByName[normalizeComparableValue(name)] = name
	}

	matches := make([]string, 0)
	seen := make(map[string]bool)
	for _, actor := range candidateActors {
		name := strings.TrimSpace(actor.Name)
		if name == "" {
			continue
		}

		if actor.ID != "" {
			if matchedName, ok := sourceByID[actor.ID]; ok && !seen[matchedName] {
				seen[matchedName] = true
				matches = append(matches, matchedName)
				continue
			}
		}

		key := normalizeComparableValue(name)
		if matchedName, ok := sourceByName[key]; ok && !seen[matchedName] {
			seen[matchedName] = true
			matches = append(matches, matchedName)
		}
	}
	return matches
}

func findVendorMatches(source *model.Media, candidate *model.Media) []vendorMatch {
	if source == nil || candidate == nil {
		return nil
	}

	type candidateVendor struct {
		Kind  string
		Value string
	}

	sourceVendors := map[string][]candidateVendor{
		"label":  {{Kind: "label", Value: source.Label}},
		"maker":  {{Kind: "maker", Value: source.Maker}},
		"studio": {{Kind: "studio", Value: source.Studio}},
	}

	candidateValues := map[string]string{
		"label":  candidate.Label,
		"maker":  candidate.Maker,
		"studio": candidate.Studio,
	}

	matches := make([]vendorMatch, 0, 3)
	for kind, vendors := range sourceVendors {
		for _, vendor := range vendors {
			sourceValue := normalizeComparableValue(vendor.Value)
			candidateValue := normalizeComparableValue(candidateValues[kind])
			if sourceValue == "" || candidateValue == "" || sourceValue != candidateValue {
				continue
			}
			matches = append(matches, vendorMatch{
				Kind:  kind,
				Value: strings.TrimSpace(candidateValues[kind]),
			})
			break
		}
	}
	return matches
}

func buildVendorReason(matches []vendorMatch) (string, []string) {
	if len(matches) == 0 {
		return "", nil
	}

	match := matches[0]
	switch match.Kind {
	case "label":
		return "同厂牌：" + match.Value, []string{match.Value}
	case "maker":
		return "同片商：" + match.Value, []string{match.Value}
	default:
		return "同制作方：" + match.Value, []string{match.Value}
	}
}

func buildExploreReason(seed *recommendationSeed, media *model.Media) string {
	if seed != nil && seed.Routes["favorite"] {
		return "已收藏的相似内容"
	}
	if media != nil && time.Since(media.CreatedAt) <= 45*24*time.Hour {
		return "最近新增"
	}
	return "资料更完整"
}

func favoriteBoostScore(seed *recommendationSeed) float64 {
	if seed != nil && seed.Routes["favorite"] {
		return 3
	}
	return 0
}

func ratingWeakScore(rating float64) float64 {
	if rating <= 0 {
		return 0
	}
	if rating > 5 {
		rating = 5
	}
	return rating / 5
}

func yearClosenessScore(sourceYear, candidateYear int) float64 {
	if sourceYear <= 0 || candidateYear <= 0 {
		return 0
	}
	diff := sourceYear - candidateYear
	if diff < 0 {
		diff = -diff
	}
	switch {
	case diff == 0:
		return 5
	case diff <= 1:
		return 4
	case diff <= 3:
		return 2
	default:
		return 0
	}
}

func freshnessScore(createdAt time.Time) float64 {
	if createdAt.IsZero() {
		return 0
	}
	days := time.Since(createdAt).Hours() / 24
	switch {
	case days <= 14:
		return 3
	case days <= 45:
		return 2
	case days <= 90:
		return 1
	default:
		return 0
	}
}

func buildJSONLikePattern(field string, value string) string {
	field = strings.TrimSpace(field)
	value = strings.TrimSpace(value)
	if field == "" || value == "" {
		return ""
	}
	return fmt.Sprintf("%%\"%s\":\"%s\"%%", field, value)
}

func buildCodeJSONLikePattern(prefix string) string {
	prefix = strings.ToUpper(strings.TrimSpace(prefix))
	if prefix == "" {
		return ""
	}
	return fmt.Sprintf("%%\"num\":\"%s-%%", prefix)
}

func normalizeComparableValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.ToLower(value)
	value = strings.ReplaceAll(value, "\u3000", " ")
	value = strings.Join(strings.Fields(value), " ")
	return value
}

func looksLikeIdentifierTag(token string, upper string) bool {
	token = strings.TrimSpace(token)
	upper = strings.TrimSpace(upper)
	if token == "" || upper == "" {
		return false
	}
	if allowedShortThemeTokens[upper] {
		return false
	}

	hasLetter := false
	for _, r := range upper {
		switch {
		case 'A' <= r && r <= 'Z':
			hasLetter = true
		case '0' <= r && r <= '9':
		case r == '.' || r == '_' || r == '-' || unicode.IsSpace(r):
		default:
			return false
		}
	}

	return hasLetter
}

func isCoreContentTag(token string, upper string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	if looksLikeIdentifierTag(token, upper) {
		return false
	}

	key := normalizeComparableValue(token)
	if key == "" || genericThemeTokens[key] {
		return false
	}

	runeCount := 0
	for range token {
		runeCount++
	}
	return runeCount >= 2
}

func dedupeCaseInsensitive(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]bool)
	for _, value := range values {
		value = strings.TrimSpace(value)
		key := normalizeComparableValue(value)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, value)
	}
	return result
}

func intersectStrings(source []string, candidate []string) []string {
	if len(source) == 0 || len(candidate) == 0 {
		return nil
	}

	sourceSet := make(map[string]string, len(source))
	for _, value := range source {
		key := normalizeComparableValue(value)
		if key != "" {
			sourceSet[key] = strings.TrimSpace(value)
		}
	}

	matches := make([]string, 0)
	seen := make(map[string]bool)
	for _, value := range candidate {
		key := normalizeComparableValue(value)
		if key == "" || seen[key] {
			continue
		}
		if sourceValue, ok := sourceSet[key]; ok {
			seen[key] = true
			matches = append(matches, sourceValue)
		}
	}
	return matches
}

func joinDisplayValues(values []string, max int) string {
	if len(values) == 0 {
		return ""
	}
	if max <= 0 || max > len(values) {
		max = len(values)
	}
	return strings.Join(values[:max], " / ")
}

func stripTagPrefix(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	for _, separator := range []string{":", "："} {
		if idx := strings.Index(value, separator); idx >= 0 {
			return strings.TrimSpace(value[idx+len(separator):])
		}
	}
	return value
}

func isStaticRecommendationCandidate(source *model.Media, candidate *model.Media) bool {
	if source == nil || candidate == nil {
		return false
	}
	if candidate.ID == "" || source.ID == candidate.ID {
		return false
	}
	if candidate.MediaType != "" && source.MediaType != "" && candidate.MediaType != source.MediaType {
		return false
	}
	if candidate.LibraryID != "" && source.LibraryID != "" && candidate.LibraryID != source.LibraryID {
		return false
	}
	if source.StackGroup != "" && candidate.StackGroup != "" && source.StackGroup == candidate.StackGroup {
		return false
	}
	if source.VersionGroup != "" && candidate.VersionGroup != "" && source.VersionGroup == candidate.VersionGroup {
		return false
	}
	return true
}
