package enrich

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

const maxPartialSummary = 1500
const maxPartialClaims = 12

// Partial is what one map call returns for a slice of a long session. The
// reduce call sees only validated partials, never the raw transcript.
type Partial struct {
	Summary   string   `json:"summary"`
	Topics    []string `json:"topics"`
	Decisions []Claim  `json:"decisions"`
	FollowUps []Claim  `json:"follow_ups"`
	Entities  Entities `json:"entities"`
}

// ParsePartial validates a map reply with the same strictness as Parse.
// Citations outside validSeqs are dropped.
func ParsePartial(raw string, validSeqs map[int]bool) (Partial, error) {
	text := strings.TrimSpace(raw)
	if m := fence.FindStringSubmatch(text); m != nil {
		text = m[1]
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(text)))
	dec.DisallowUnknownFields()
	var p Partial
	if err := dec.Decode(&p); err != nil {
		var syn *json.SyntaxError
		var typ *json.UnmarshalTypeError
		switch {
		case errors.As(err, &syn) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF):
			return Partial{}, &ParseError{"not_json"}
		case errors.As(err, &typ):
			return Partial{}, &ParseError{"schema_type"}
		default:
			return Partial{}, &ParseError{"schema"}
		}
	}
	if dec.More() {
		return Partial{}, &ParseError{"trailing_output"}
	}
	var q Quality
	p.Summary = trunc(strings.TrimSpace(p.Summary), maxPartialSummary, &q)
	if p.Summary == "" {
		return Partial{}, &ParseError{"missing_required"}
	}
	p.Topics = tags(p.Topics, &q)
	p.Decisions = capClaims(claims(p.Decisions, validSeqs, &q), maxPartialClaims)
	p.FollowUps = capClaims(claims(p.FollowUps, validSeqs, &q), maxPartialClaims)
	p.Entities = Entities{Projects: names(p.Entities.Projects, &q), Repos: names(p.Entities.Repos, &q),
		Tools: names(p.Entities.Tools, &q), Technologies: names(p.Entities.Technologies, &q)}
	return p, nil
}

func capClaims(in []Claim, max int) []Claim {
	if len(in) > max {
		return in[:max]
	}
	return in
}
