package transcript

import "time"

// Exports for the Postgres read side (package pgstore). They expose the browse
// helpers that are independent of any database, so both implementations return
// identical shapes.

// ParseEnrichment reads a stored Tier 1 result into its view.
func ParseEnrichment(resultJSON string) (EnrichmentView, bool) { return parseEnrichment(resultJSON) }

// SearchTerms lowercases and de-duplicates the words of a query.
func SearchTerms(text string) []string { return searchTerms(text) }

// MatchSummaryJSON scores a stored Tier 1 result against terms; every term must
// occur somewhere in the result.
func MatchSummaryJSON(resultJSON string, terms []string) (int, Match, bool) {
	return matchSummary(summaryEntries(resultJSON), terms)
}

// SummaryLeadOf is the start of a stored summary, shown beside a semantic hit.
func SummaryLeadOf(resultJSON string) string { return summaryLead(resultJSON) }

func ClampLimit(limit int) int       { return clampLimit(limit) }
func DecodeCursor(cursor string) int { return decodeCursor(cursor) }
func EncodeCursor(offset int) string { return encodeCursor(offset) }
func FirstLine(text string, max int) string {
	return firstLine(text, max)
}
func Untitled(item SessionSummary) string { return untitled(item) }
func Stamp(t time.Time) string            { return stamp(t) }

// Limits shared with the SQLite read side.
const (
	MaxSnippetBytes = maxSnippetBytes
	MaxTurnTextRead = maxTurnTextRead
	MaxTurnPage     = maxTurnPage
	MaxTreeNodes    = maxTreeNodes
	MaxSuggestLimit = maxSuggestLimit
	MaxRankedLimit  = maxRankedResults
	MaxFindLimit    = maxFindLimit
	MaxErrorItems   = maxErrorItems
	MaxStatDays     = maxStatDays
	MaxStatProjects = maxStatProjects
)

// Deadlines shared with the SQLite read side.
const (
	SearchDeadline   = searchDeadline
	SummaryDeadline  = summarySearchDeadline
	SemanticDeadline = semanticScanDeadline
	FindDeadline     = findDeadline
	StatsDeadline    = statsDeadline
)
