// Package enrich turns redacted private transcripts into structured, searchable
// summaries. Tier 0 is deterministic facts; Tier 1 asks a model, through the
// Tether AI gateway, for a bounded JSON description of one session.
package enrich

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/hollis-labs/fragments-engine/internal/transcript"
)

// PromptVersion names the prompt and output contract. Changing either bumps it,
// which makes every session pending again.
const PromptVersion = "enrich-v1"

const (
	maxTitle     = 80
	maxSummary   = 1200
	maxClaim     = 300
	maxTopics    = 8
	maxClaims    = 8
	maxEntities  = 12
	maxEntityLen = 60
)

var sessionTypes = map[string]bool{"coding": true, "debugging": true, "design": true, "research": true, "ops": true, "writing": true, "planning": true, "chat": true, "other": true}
var outcomes = map[string]bool{"completed": true, "partial": true, "abandoned": true, "unclear": true}

type Claim struct {
	Text  string `json:"text"`
	Turns []int  `json:"turns"`
}

type Entities struct {
	Projects     []string `json:"projects"`
	Repos        []string `json:"repos"`
	Tools        []string `json:"tools"`
	Technologies []string `json:"technologies"`
}

// Result is the Tier 1 output contract.
type Result struct {
	SchemaVersion int      `json:"schema_version"`
	Title         string   `json:"title"`
	Summary       string   `json:"summary"`
	SessionType   string   `json:"session_type"`
	Outcome       string   `json:"outcome"`
	Topics        []string `json:"topics"`
	Decisions     []Claim  `json:"decisions"`
	FollowUps     []Claim  `json:"follow_ups"`
	Entities      Entities `json:"entities"`
	Language      string   `json:"language"`
}

// Quality records how the output measured against its source; it is metadata
// about the run, never transcript content.
type Quality struct {
	UngroundedCites    int    `json:"ungrounded_cites"`
	UngroundedEntities int    `json:"ungrounded_entities"`
	Truncated          int    `json:"truncated"`
	Retries            int    `json:"retries"`
	Cites              int    `json:"cites"`
	EntitiesChecked    int    `json:"entities_checked"`
	ResponseModel      string `json:"response_model,omitempty"`
}

// ParseError carries only a fixed class, never model output.
type ParseError struct{ Class string }

func (e *ParseError) Error() string { return "enrichment output invalid: " + e.Class }

var fence = regexp.MustCompile("(?s)^\\s*```[a-zA-Z]*\\s*(.*?)\\s*```\\s*$")
var tagChars = regexp.MustCompile(`[^a-z0-9-]+`)

// Parse validates raw model output against the contract. validSeqs is the set
// of turn indexes that exist; citations outside it are dropped and counted.
// source is the lowercase transcript text used for the entity grounding check.
func Parse(raw string, validSeqs map[int]bool, source string) (Result, Quality, error) {
	var q Quality
	text := strings.TrimSpace(raw)
	if m := fence.FindStringSubmatch(text); m != nil {
		text = m[1]
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(text)))
	dec.DisallowUnknownFields()
	var r Result
	if err := dec.Decode(&r); err != nil {
		var syn *json.SyntaxError
		var typ *json.UnmarshalTypeError
		switch {
		case errors.As(err, &syn) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF):
			return Result{}, q, &ParseError{"not_json"}
		case errors.As(err, &typ):
			return Result{}, q, &ParseError{"schema_type"}
		default:
			return Result{}, q, &ParseError{"schema"}
		}
	}
	if dec.More() {
		return Result{}, q, &ParseError{"trailing_output"}
	}
	if r.SchemaVersion != 0 && r.SchemaVersion != 1 {
		return Result{}, q, &ParseError{"schema_version"}
	}
	r.SchemaVersion = 1
	r.Title = trunc(strings.TrimSpace(r.Title), maxTitle, &q)
	r.Summary = trunc(strings.TrimSpace(r.Summary), maxSummary, &q)
	if r.Title == "" || r.Summary == "" {
		return Result{}, q, &ParseError{"missing_required"}
	}
	r.SessionType = strings.ToLower(strings.TrimSpace(r.SessionType))
	r.Outcome = strings.ToLower(strings.TrimSpace(r.Outcome))
	if !sessionTypes[r.SessionType] || !outcomes[r.Outcome] {
		return Result{}, q, &ParseError{"enum"}
	}
	r.Language = trunc(strings.ToLower(strings.TrimSpace(r.Language)), 16, &q)
	r.Topics = tags(r.Topics, &q)
	r.Decisions = claims(r.Decisions, validSeqs, &q)
	r.FollowUps = claims(r.FollowUps, validSeqs, &q)
	r.Entities = Entities{
		Projects: names(r.Entities.Projects, &q), Repos: names(r.Entities.Repos, &q),
		Tools: names(r.Entities.Tools, &q), Technologies: names(r.Entities.Technologies, &q),
	}
	// Grounding: a project or repo the session never mentions is a hallucination.
	for _, group := range [][]string{r.Entities.Projects, r.Entities.Repos} {
		for _, e := range group {
			q.EntitiesChecked++
			if !strings.Contains(source, strings.ToLower(e)) {
				q.UngroundedEntities++
			}
		}
	}
	return redactResult(r), q, nil
}

func trunc(s string, max int, q *Quality) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	q.Truncated++
	r := []rune(s)
	return string(r[:max])
}

func tags(in []string, q *Quality) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range in {
		t = tagChars.ReplaceAllString(strings.ReplaceAll(strings.ToLower(strings.TrimSpace(t)), " ", "-"), "")
		t = strings.Trim(t, "-")
		if t == "" || len(t) > 40 || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	if len(out) > maxTopics {
		q.Truncated++
		out = out[:maxTopics]
	}
	return out
}

func names(in []string, q *Quality) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range in {
		n = trunc(strings.TrimSpace(n), maxEntityLen, q)
		key := strings.ToLower(n)
		if n == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, n)
	}
	if len(out) > maxEntities {
		q.Truncated++
		out = out[:maxEntities]
	}
	return out
}

func claims(in []Claim, valid map[int]bool, q *Quality) []Claim {
	var out []Claim
	for _, c := range in {
		c.Text = trunc(strings.TrimSpace(c.Text), maxClaim, q)
		if c.Text == "" {
			continue
		}
		var turns []int
		seen := map[int]bool{}
		for _, t := range c.Turns {
			q.Cites++
			if !valid[t] {
				q.UngroundedCites++
				continue
			}
			if !seen[t] {
				seen[t] = true
				turns = append(turns, t)
			}
		}
		c.Turns = turns
		out = append(out, c)
	}
	if len(out) > maxClaims {
		q.Truncated++
		out = out[:maxClaims]
	}
	return out
}

// redactResult passes every string through the store's redaction, so a model
// that echoes a secret cannot get it persisted or indexed.
func redactResult(r Result) Result {
	r.Title, r.Summary = transcript.Redact(r.Title), transcript.Redact(r.Summary)
	for i := range r.Topics {
		r.Topics[i] = transcript.Redact(r.Topics[i])
	}
	for _, list := range []*[]Claim{&r.Decisions, &r.FollowUps} {
		for i := range *list {
			(*list)[i].Text = transcript.Redact((*list)[i].Text)
		}
	}
	for _, p := range []*[]string{&r.Entities.Projects, &r.Entities.Repos, &r.Entities.Tools, &r.Entities.Technologies} {
		for i := range *p {
			(*p)[i] = transcript.Redact((*p)[i])
		}
	}
	return r
}
