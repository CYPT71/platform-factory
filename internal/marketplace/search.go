package marketplace

import (
	"sort"
	"strings"
	"time"
)

// SortBy orders search results.
type SortBy string

const (
	SortRelevance  SortBy = "relevance"
	SortPopularity SortBy = "popularity"
	SortVerified   SortBy = "verified"
	SortName       SortBy = "name"
	SortDate       SortBy = "date"
)

// Filter narrows a search independently of the query text.
type Filter struct {
	VerifiedOnly bool
	Tag          string // exact, case-insensitive tag match; "" means no filter
}

// Request describes one search: free-text query, filters, sort order, and
// a 1-based page.
type Request struct {
	Query    string
	Filter   Filter
	Sort     SortBy
	Page     int // defaults to 1
	PageSize int // defaults to 20
}

// Hit pairs a plugin with its match score.
type Hit struct {
	Plugin PluginEntry
	Score  int
}

// Result contains one page and its pagination metadata.
type Result struct {
	Hits       []Hit
	Total      int
	Page       int
	PageSize   int
	TotalPages int
}

// Search performs fuzzy matching, filtering, sorting, and pagination.
func Search(index *Index, req Request) Result {
	page := req.Page
	if page < 1 {
		page = 1
	}
	pageSize := req.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}
	query := strings.TrimSpace(req.Query)
	sortBy := req.Sort
	if sortBy == "" {
		sortBy = SortRelevance
	}
	if query == "" && sortBy == SortRelevance {
		sortBy = SortName
	}

	var hits []Hit
	for _, plugin := range index.Plugins {
		if req.Filter.VerifiedOnly && !anyVerified(plugin) {
			continue
		}
		if req.Filter.Tag != "" && !hasTag(plugin, req.Filter.Tag) {
			continue
		}
		if query == "" {
			hits = append(hits, Hit{Plugin: plugin, Score: 0})
			continue
		}
		if score, ok := matchPlugin(plugin, query); ok {
			hits = append(hits, Hit{Plugin: plugin, Score: score})
		}
	}

	sortHits(hits, sortBy)

	total := len(hits)
	totalPages := (total + pageSize - 1) / pageSize
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	return Result{
		Hits:       append([]Hit(nil), hits[start:end]...),
		Total:      total,
		Page:       page,
		PageSize:   pageSize,
		TotalPages: totalPages,
	}
}

func anyVerified(plugin PluginEntry) bool {
	for _, release := range plugin.Releases {
		if release.Verified {
			return true
		}
	}
	return false
}

func hasTag(plugin PluginEntry, tag string) bool {
	for _, candidate := range plugin.Tags {
		if strings.EqualFold(candidate, tag) {
			return true
		}
	}
	return false
}

func sortHits(hits []Hit, sortBy SortBy) {
	sort.SliceStable(hits, func(i, j int) bool {
		switch sortBy {
		case SortPopularity:
			if hits[i].Plugin.Downloads != hits[j].Plugin.Downloads {
				return hits[i].Plugin.Downloads > hits[j].Plugin.Downloads
			}
		case SortDate:
			ti, tj := latestPublishedAt(hits[i].Plugin), latestPublishedAt(hits[j].Plugin)
			if !ti.Equal(tj) {
				return ti.After(tj)
			}
		case SortVerified:
			vi, vj := anyVerified(hits[i].Plugin), anyVerified(hits[j].Plugin)
			if vi != vj {
				return vi
			}
		case SortName:
			// falls through to the name tiebreak below
		default: // SortRelevance
			if hits[i].Score != hits[j].Score {
				return hits[i].Score > hits[j].Score
			}
		}
		return hits[i].Plugin.Name < hits[j].Plugin.Name
	})
}

func latestPublishedAt(plugin PluginEntry) (latest time.Time) {
	for _, release := range plugin.Releases {
		if release.PublishedAt.After(latest) {
			latest = release.PublishedAt
		}
	}
	return latest
}

// Field weights prefer names, then tags, authors, and descriptions.
const (
	weightName        = 100
	weightTag         = 60
	weightAuthor      = 40
	weightDescription = 20
)

// matchPlugin returns the best matching field score.
func matchPlugin(plugin PluginEntry, query string) (score int, ok bool) {
	best := 0
	matched := false
	consider := func(field string, weight int) {
		if s, fieldOK := fuzzyScore(field, query); fieldOK {
			matched = true
			if total := s + weight; total > best {
				best = total
			}
		}
	}
	consider(plugin.Name, weightName)
	consider(plugin.Description, weightDescription)
	consider(plugin.Author, weightAuthor)
	for _, tag := range plugin.Tags {
		consider(tag, weightTag)
	}
	return best, matched
}

// fuzzyScore rewards early, consecutive case-insensitive subsequences.
func fuzzyScore(target, query string) (score int, ok bool) {
	target = strings.ToLower(target)
	query = strings.ToLower(query)
	if query == "" {
		return 0, false
	}
	if strings.Contains(target, query) {
		score = 50 + (100 - min(strings.Index(target, query), 100))
		if target == query {
			score += 100
		}
		return score, true
	}
	targetRunes := []rune(target)
	queryRunes := []rune(query)
	queryIndex := 0
	consecutive := 0
	for i, r := range targetRunes {
		if queryIndex >= len(queryRunes) {
			break
		}
		if r == queryRunes[queryIndex] {
			consecutive++
			score += consecutive
			if i == 0 {
				score += 5
			}
			queryIndex++
		} else {
			consecutive = 0
		}
	}
	if queryIndex < len(queryRunes) {
		return 0, false
	}
	return score, true
}
