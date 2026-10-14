package enrich

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/transcript"
)

// Embedder turns text into a vector. The default is a local Ollama, so no
// transcript-derived text leaves the machine for embedding.
type Embedder interface {
	Embed(ctx context.Context, model string, texts []string) ([][]float32, error)
}

// OllamaEmbedder calls a local Ollama /api/embed.
type OllamaEmbedder struct {
	Host   string
	Client *http.Client
}

func NewOllamaEmbedder(host string) (*OllamaEmbedder, error) {
	host = strings.TrimRight(strings.TrimSpace(host), "/")
	if host == "" {
		host = "http://localhost:11434"
	}
	if !(strings.HasPrefix(host, "http://localhost:") || strings.HasPrefix(host, "http://127.0.0.1:") || strings.HasPrefix(host, "http://[::1]:")) {
		return nil, errors.New("embeddings: the Ollama host must be on this machine")
	}
	return &OllamaEmbedder{Host: host, Client: &http.Client{Timeout: 120 * time.Second}}, nil
}

func (e *OllamaEmbedder) Embed(ctx context.Context, model string, texts []string) ([][]float32, error) {
	// "prompt" matches what the recall index sends; current Ollama reads "input".
	body, _ := json.Marshal(map[string]any{"model": model, "input": texts, "prompt": texts})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.Host+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("embeddings: bad request")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.Client.Do(req)
	if err != nil {
		return nil, errors.New("embeddings: Ollama is unreachable")
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embeddings: Ollama answered %d", resp.StatusCode)
	}
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if json.Unmarshal(data, &out) != nil || len(out.Embeddings) != len(texts) {
		return nil, errors.New("embeddings: unexpected Ollama response")
	}
	return out.Embeddings, nil
}

// EmbedSummary counts one embedding pass.
type EmbedSummary struct {
	Candidates, Skipped, Embedded, Failed int
}

// EmbedSummaries embeds the newest ok Tier 1 result of every session whose
// vector is missing or stale. limit 0 means all. It never calls a model.
func EmbedSummaries(ctx context.Context, st *transcript.Store, e Embedder, model string, limit int) (EmbedSummary, error) {
	var sum EmbedSummary
	texts, err := st.SummaryTexts(ctx)
	if err != nil {
		return sum, err
	}
	sum.Candidates = len(texts)
	var todo []transcript.SummaryText
	for _, t := range texts {
		if t.Text == "" {
			sum.Skipped++
			continue
		}
		ok, err := st.EmbeddingCurrent(ctx, t.SessionPK, "summary", 0, model, t.Digest)
		if err != nil {
			return sum, err
		}
		if ok {
			sum.Skipped++
			continue
		}
		todo = append(todo, t)
	}
	if limit > 0 && len(todo) > limit {
		todo = todo[:limit]
	}
	const batch = 16
	for i := 0; i < len(todo); i += batch {
		end := i + batch
		if end > len(todo) {
			end = len(todo)
		}
		group := todo[i:end]
		in := make([]string, len(group))
		for j, g := range group {
			in[j] = g.Text
		}
		vecs, err := e.Embed(ctx, model, in)
		if err != nil {
			sum.Failed += len(group)
			return sum, err
		}
		for j, g := range group {
			if err := st.SaveEmbedding(ctx, g.SessionPK, "summary", 0, model, g.Digest, vecs[j]); err != nil {
				sum.Failed++
				continue
			}
			sum.Embedded++
		}
	}
	return sum, nil
}
