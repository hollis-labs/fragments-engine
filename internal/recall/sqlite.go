package recall

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/extract"
	"github.com/hollis-labs/fragments-engine/internal/repository"
)

type SQLiteIndexer struct {
	fragments *repository.FragmentRepository
	entities  *repository.EntityRepository
}

func NewSQLiteIndexer(fragments *repository.FragmentRepository, entities *repository.EntityRepository) *SQLiteIndexer {
	return &SQLiteIndexer{fragments: fragments, entities: entities}
}

func (s *SQLiteIndexer) IndexFragment(ctx context.Context, fragment domain.Fragment) error {
	summary := summarize(fragment)
	if err := s.fragments.UpdateIndexMetadata(ctx, fragment.ID, summary, time.Now().UTC()); err != nil {
		return err
	}
	fragmentWithSummary := fragment
	fragmentWithSummary.Summary = summary
	fragmentEntities := extract.FromFragment(fragmentWithSummary)
	if s.entities != nil {
		if err := s.entities.ReplaceFragmentEntities(ctx, fragment.ID, fragmentEntities); err != nil {
			return err
		}
	}

	candidates, err := s.fragments.FindRelationCandidates(ctx, fragment, 5)
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		now := time.Now().UTC()
		meta, _ := json.Marshal(map[string]any{
			"source":      fragment.Source,
			"source_type": fragment.SourceType,
		})
		link := domain.FragmentRelation{
			FragmentID:        fragment.ID,
			RelatedFragmentID: candidate.ID,
			Kind:              "shared_source_type",
			Score:             0.6,
			MetadataJSON:      string(meta),
			CreatedAt:         now,
		}
		if err := s.fragments.UpsertRelation(ctx, link); err != nil {
			return err
		}
		reverse := link
		reverse.FragmentID = candidate.ID
		reverse.RelatedFragmentID = fragment.ID
		if err := s.fragments.UpsertRelation(ctx, reverse); err != nil {
			return err
		}

		sharedTerms := sharedRelationTerms(fragment, candidate)
		if len(sharedTerms) == 0 {
			continue
		}
		sharedMeta, _ := json.Marshal(map[string]any{
			"shared_terms": sharedTerms,
			"term_count":   len(sharedTerms),
		})
		sharedScore := 0.7 + (0.05 * float64(len(sharedTerms)))
		if sharedScore > 0.9 {
			sharedScore = 0.9
		}
		sharedLink := domain.FragmentRelation{
			FragmentID:        fragment.ID,
			RelatedFragmentID: candidate.ID,
			Kind:              "shared_terms",
			Score:             sharedScore,
			MetadataJSON:      string(sharedMeta),
			CreatedAt:         time.Now().UTC(),
		}
		if err := s.fragments.UpsertRelation(ctx, sharedLink); err != nil {
			return err
		}
		sharedReverse := sharedLink
		sharedReverse.FragmentID = candidate.ID
		sharedReverse.RelatedFragmentID = fragment.ID
		if err := s.fragments.UpsertRelation(ctx, sharedReverse); err != nil {
			return err
		}

		topicTerms := sharedTopicTerms(fragment, candidate)
		if len(topicTerms) == 0 {
			continue
		}
		topicMeta, _ := json.Marshal(map[string]any{
			"topic_terms": topicTerms,
			"term_count":  len(topicTerms),
		})
		topicLink := domain.FragmentRelation{
			FragmentID:        fragment.ID,
			RelatedFragmentID: candidate.ID,
			Kind:              "shared_topic_terms",
			Score:             0.85,
			MetadataJSON:      string(topicMeta),
			CreatedAt:         now,
		}
		if err := s.fragments.UpsertRelation(ctx, topicLink); err != nil {
			return err
		}
		topicReverse := topicLink
		topicReverse.FragmentID = candidate.ID
		topicReverse.RelatedFragmentID = fragment.ID
		if err := s.fragments.UpsertRelation(ctx, topicReverse); err != nil {
			return err
		}

		sharedEntities := extract.SharedByKind(fragmentEntities, extract.FromFragment(candidate))
		for kind, values := range sharedEntities {
			relation, err := entityRelation(fragment.ID, candidate.ID, kind, values, now)
			if err != nil {
				return err
			}
			if relation.Kind == "" {
				continue
			}
			if err := s.fragments.UpsertRelation(ctx, relation); err != nil {
				return err
			}
			reverseEntity := relation
			reverseEntity.FragmentID = candidate.ID
			reverseEntity.RelatedFragmentID = fragment.ID
			if err := s.fragments.UpsertRelation(ctx, reverseEntity); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *SQLiteIndexer) Search(ctx context.Context, query string, limit int) ([]domain.SearchResult, error) {
	return s.fragments.Search(ctx, query, limit)
}

func (s *SQLiteIndexer) Related(ctx context.Context, fragmentID string, limit int) ([]domain.SearchResult, error) {
	return s.fragments.ListRelated(ctx, fragmentID, limit)
}

func (s *SQLiteIndexer) GetFragment(ctx context.Context, fragmentID string) (domain.Fragment, error) {
	return s.fragments.GetByID(ctx, fragmentID)
}

func (s *SQLiteIndexer) ListEntities(ctx context.Context, kind string, limit int) ([]repository.EntityRecord, error) {
	return s.entities.List(ctx, kind, limit)
}

func (s *SQLiteIndexer) ListFragmentsByEntity(ctx context.Context, kind, value string, limit int) ([]domain.SearchResult, error) {
	return s.entities.ListFragments(ctx, kind, value, limit)
}

func (s *SQLiteIndexer) Status() Status {
	return Status{
		Backend:         "sqlite",
		RecallMode:      "fts",
		FallbackBackend: "",
	}
}

func (s *SQLiteIndexer) Close() error {
	return nil
}

func summarize(fragment domain.Fragment) string {
	content := strings.TrimSpace(fragment.Content)
	if content == "" {
		return fragment.Title
	}
	content = strings.ReplaceAll(content, "\n", " ")
	content = strings.Join(strings.Fields(content), " ")
	if len(content) > 220 {
		content = content[:220]
	}
	if strings.TrimSpace(fragment.Title) == "" {
		return content
	}
	return fmt.Sprintf("%s: %s", fragment.Title, content)
}

func sharedRelationTerms(left, right domain.Fragment) []string {
	leftTerms := relationTerms(left)
	rightTerms := relationTerms(right)
	if len(leftTerms) == 0 || len(rightTerms) == 0 {
		return nil
	}
	shared := make([]string, 0, 4)
	for term := range leftTerms {
		if _, ok := rightTerms[term]; ok {
			shared = append(shared, term)
		}
	}
	sort.Strings(shared)
	if len(shared) > 6 {
		shared = shared[:6]
	}
	return shared
}

func relationTerms(fragment domain.Fragment) map[string]struct{} {
	text := strings.ToLower(strings.TrimSpace(fragment.Title + " " + fragment.Summary))
	if text == "" {
		text = strings.ToLower(strings.TrimSpace(fragment.Content))
	}
	text = strings.NewReplacer(
		"-", " ",
		"_", " ",
		"/", " ",
		".", " ",
		",", " ",
		":", " ",
		";", " ",
		"(", " ",
		")", " ",
		"[", " ",
		"]", " ",
	).Replace(text)

	terms := make(map[string]struct{})
	for _, term := range strings.Fields(text) {
		term = strings.TrimSpace(term)
		if len(term) < 4 {
			continue
		}
		if _, blocked := relationStopWords[term]; blocked {
			continue
		}
		terms[term] = struct{}{}
	}
	return terms
}

func sharedTopicTerms(left, right domain.Fragment) []string {
	leftTerms := topicTerms(left)
	rightTerms := topicTerms(right)
	if len(leftTerms) == 0 || len(rightTerms) == 0 {
		return nil
	}
	shared := make([]string, 0, 3)
	for term := range leftTerms {
		if _, ok := rightTerms[term]; ok {
			shared = append(shared, term)
		}
	}
	sort.Strings(shared)
	if len(shared) > 4 {
		shared = shared[:4]
	}
	return shared
}

func topicTerms(fragment domain.Fragment) map[string]struct{} {
	text := strings.ToLower(strings.TrimSpace(fragment.Title + " " + fragment.Summary))
	text = strings.NewReplacer(
		"-", " ",
		"_", " ",
		"/", " ",
		".", " ",
		",", " ",
		":", " ",
		";", " ",
		"(", " ",
		")", " ",
		"[", " ",
		"]", " ",
	).Replace(text)

	terms := make(map[string]struct{})
	for _, term := range strings.Fields(text) {
		term = strings.TrimSpace(term)
		if len(term) < 5 {
			continue
		}
		if _, blocked := topicStopWords[term]; blocked {
			continue
		}
		terms[term] = struct{}{}
	}
	return terms
}

var relationStopWords = map[string]struct{}{
	"about":     {},
	"assistant": {},
	"chat":      {},
	"claude":    {},
	"fragment":  {},
	"fragments": {},
	"review":    {},
	"session":   {},
	"starts":    {},
	"summary":   {},
	"their":     {},
	"there":     {},
	"these":     {},
	"with":      {},
}

var topicStopWords = map[string]struct{}{
	"assistant": {},
	"claude":    {},
	"followup":  {},
	"fragments": {},
	"review":    {},
	"session":   {},
	"starts":    {},
	"summary":   {},
}

func entityRelation(fragmentID, relatedID, kind string, values []string, createdAt time.Time) (domain.FragmentRelation, error) {
	if len(values) == 0 {
		return domain.FragmentRelation{}, nil
	}
	metaJSON, err := json.Marshal(map[string]any{
		"entity_kind": kind,
		"values":      values,
		"value_count": len(values),
	})
	if err != nil {
		return domain.FragmentRelation{}, err
	}
	return domain.FragmentRelation{
		FragmentID:        fragmentID,
		RelatedFragmentID: relatedID,
		Kind:              relationKindForEntity(kind),
		Score:             relationScoreForEntity(kind),
		MetadataJSON:      string(metaJSON),
		CreatedAt:         createdAt,
	}, nil
}

func relationKindForEntity(kind string) string {
	switch kind {
	case "repo":
		return "shared_repo"
	case "workspace":
		return "shared_workspace"
	case "model":
		return "shared_model"
	case "tool":
		return "shared_tool"
	default:
		return ""
	}
}

func relationScoreForEntity(kind string) float64 {
	switch kind {
	case "repo":
		return 0.95
	case "workspace":
		return 0.92
	case "model":
		return 0.88
	case "tool":
		return 0.82
	default:
		return 0
	}
}
