package enrich

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
)

// OpenAICompatEmbedder talks to a local OpenAI-style POST /v1/embeddings server
// (llama.cpp --embeddings, for example). It needs no key and refuses any host
// that is not on this machine.
type OpenAICompatEmbedder struct {
	BaseURL string
	Client  *http.Client
	// Dim, when set, is the dimension every vector must have.
	Dim int
	// QueryInstruction, when set, frames search queries as
	// "Instruct: <task>\nQuery: <q>" for instruction-aware models such as
	// Qwen3-Embedding. Documents are always embedded as plain text.
	QueryInstruction string
	BatchSize        int
}

// NewOpenAICompatEmbedder validates the URL. Anything but a loopback http URL
// fails closed.
func NewOpenAICompatEmbedder(baseURL string, dim int, queryInstruction string, batch int) (*OpenAICompatEmbedder, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if !isLoopbackHTTP(baseURL) {
		return nil, errors.New("embeddings: base_url must be an http URL on this machine (localhost, 127.0.0.1 or ::1 with a port)")
	}
	if batch <= 0 {
		batch = 16
	}
	return &OpenAICompatEmbedder{BaseURL: baseURL, Client: &http.Client{Timeout: 120 * time.Second}, Dim: dim, QueryInstruction: queryInstruction, BatchSize: batch}, nil
}

// Format identifies how documents are prepared; it is stored with each vector.
func (e *OpenAICompatEmbedder) Format() string { return "doc-plain/v1" }

func (e *OpenAICompatEmbedder) Embed(ctx context.Context, model string, texts []string) ([][]float32, error) {
	out := make([][]float32, 0, len(texts))
	for i := 0; i < len(texts); i += e.BatchSize {
		end := i + e.BatchSize
		if end > len(texts) {
			end = len(texts)
		}
		vecs, err := e.batch(ctx, model, texts[i:end])
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}

// EmbedQuery applies the query instruction, if configured.
func (e *OpenAICompatEmbedder) EmbedQuery(ctx context.Context, model, query string) ([]float32, error) {
	text := query
	if e.QueryInstruction != "" {
		text = "Instruct: " + e.QueryInstruction + "\nQuery: " + query
	}
	v, err := e.batch(ctx, model, []string{text})
	if err != nil {
		return nil, err
	}
	return v[0], nil
}

func (e *OpenAICompatEmbedder) batch(ctx context.Context, model string, texts []string) ([][]float32, error) {
	body, _ := json.Marshal(map[string]any{"model": model, "input": texts})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.BaseURL+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("embeddings: bad request")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.Client.Do(req)
	if err != nil {
		return nil, errors.New("embeddings: the local embedding server is unreachable")
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 128<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embeddings: the local server answered %d", resp.StatusCode)
	}
	var parsed struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if json.Unmarshal(data, &parsed) != nil || len(parsed.Data) != len(texts) {
		return nil, errors.New("embeddings: unexpected response shape")
	}
	sort.Slice(parsed.Data, func(i, j int) bool { return parsed.Data[i].Index < parsed.Data[j].Index })
	out := make([][]float32, len(texts))
	dim := e.Dim
	for i, d := range parsed.Data {
		if d.Index != i || len(d.Embedding) == 0 {
			return nil, errors.New("embeddings: missing or empty vector")
		}
		if dim == 0 {
			dim = len(d.Embedding)
		}
		if len(d.Embedding) != dim {
			return nil, fmt.Errorf("embeddings: vector dimension %d, expected %d", len(d.Embedding), dim)
		}
		for _, f := range d.Embedding {
			if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
				return nil, errors.New("embeddings: non-finite value in vector")
			}
		}
		out[i] = d.Embedding
	}
	return out, nil
}
