# Go Classification Libraries — Research

Researched 2026-04-25. For the Fragments Engine triage / classification layer.

## Recommended Stack

| Layer | Library | Notes |
|---|---|---|
| Rule/keyword routing | stdlib `regexp` + hand-rolled scorer | Zero deps, first choice |
| Bayesian multi-class | `jbrukh/bayesian` | ~800 stars, active, TF-IDF weighting |
| Bayesian multi-label | `lytics/multibayes` | ~300 stars; when a fragment can match multiple routes |
| NLP primitives (tokenize, NER) | `jdkato/prose` | ~3k stars; archived but stable; covers 80% of NLP needs |
| TF-IDF / semantic similarity | `james-bowman/nlp` | ~466 stars; LSA + sparse matrix pipelines |
| Language detection | `pemistahl/lingua-go` | ~1.2k stars, actively maintained |
| Content-level dedup | `hbollon/go-edlib` | Full edit-distance suite: Levenshtein, Jaro-Winkler, Cosine, LCS |
| Title/fuzzy dedup | `sahilm/fuzzy` | ~1k stars, Sublime-style, zero deps |
| ML / ONNX (optional) | `knights-analytics/hugot` | Hugging Face transformer pipelines locally via ONNX; requires CGo |

## By Category

### 1. Rule-Based / Deterministic

Go's `regexp` stdlib is sufficient for keyword and pattern matching. The classifier engine is a hand-rolled weighted scorer:

```go
type Classifier struct {
    Pattern *regexp.Regexp
    Tag     string
    Weight  float64
}

func Score(text string, classifiers []Classifier) map[string]float64 { ... }
```

For Bayesian:
- **`github.com/jbrukh/bayesian`** — Train on user-routed examples; multi-class; TF-IDF. The right default for learned routing.
- **`github.com/lytics/multibayes`** — Multi-label variant; use when a fragment can go to multiple destinations simultaneously (e.g., both Vanta KB and Nil).

### 2. NLP Primitives

- **`github.com/jdkato/prose/v2`** — Tokenization, POS tagging, named entity recognition (persons, places, orgs). Archived but widely used and stable. Best for entity extraction from chat turns.
- **`github.com/james-bowman/nlp`** — TF-IDF vectorization, LSA. Use for semantic similarity scoring between fragments (dedup, related-item linking).
- **`github.com/pemistahl/lingua-go`** — Language detection. Route non-English fragments differently if needed.

### 3. Fuzzy / Dedup

- **`github.com/hbollon/go-edlib`** — Full edit-distance suite. Use for content-level near-duplicate detection (e.g., two ChatGPT exports of the same conversation).
- **`github.com/sahilm/fuzzy`** — Title-level dedup and search. Also useful for inbox search UI.

### 4. Local ML (Optional, CGo)

- **`github.com/knights-analytics/hugot`** — Runs Hugging Face transformer models via ONNX Runtime locally. Gives zero-shot classification ("is this a code fragment or a note?") without calling an API. Requires the ONNX Runtime C library and CGo.

Use only if the deterministic + Bayesian stack can't handle the ambiguity. CGo complicates builds and cross-compilation.

## Decision Notes

- **Start deterministic**: source-type rules + regex cover the common cases (git commits are always code; Claude sessions are always conversations).
- **Add Bayesian early**: it trains on user routing decisions from day one and compounds value.
- **Defer ML**: bring in `hugot` only if edge cases accumulate that Bayesian can't handle (e.g., distinguishing an article save from a note save when both come from the same web clipper source).
- **AI enrichment is separate**: calling Claude/Ollama for summaries and context hints is an enrichment step, not a classification step. It runs async and does not block routing.
