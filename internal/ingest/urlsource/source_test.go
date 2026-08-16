package urlsource

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/extract"
)

func TestSourceCollect_HTMLAndPDF(t *testing.T) {
	pdfFixture, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "urlsource-sample.pdf"))
	if err != nil {
		t.Fatalf("read pdf fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/article":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<!doctype html><html><head><title>Roadmap Review</title></head><body><main><h1>Roadmap</h1><p>The roadmap starts with deterministic ingest and search.</p></main></body></html>`))
		case "/guide.pdf":
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write(pdfFixture)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	root := t.TempDir()
	manifest := filepath.Join(root, "urls.txt")
	if err := os.WriteFile(manifest, []byte(strings.Join([]string{
		srv.URL + "/article",
		srv.URL + "/guide.pdf",
	}, "\n")), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	fragments, err := Source{now: func() time.Time { return time.Date(2026, 4, 27, 18, 0, 0, 0, time.UTC) }}.Collect(context.Background(), config.IngestConfig{
		Name:   "urls-test",
		Kind:   kind,
		Source: config.IngestSource{Root: root},
		Routing: config.IngestRouting{
			Namespace: "fragments/web",
		},
		Rules: map[string]any{
			"request_timeout_seconds": 10,
			"max_body_mb":             1,
		},
	})
	if err != nil {
		t.Fatalf("collect urls: %v", err)
	}
	if len(fragments) != 2 {
		t.Fatalf("expected 2 fragments, got %d", len(fragments))
	}
	article := fragments[0]
	if article.Source != "url" || article.SourceType != "article" {
		t.Fatalf("unexpected article source fields: %+v", article)
	}
	if article.Title != "Roadmap Review" {
		t.Fatalf("unexpected article title: %s", article.Title)
	}
	if !strings.Contains(article.Content, "deterministic ingest and search") {
		t.Fatalf("expected extracted article text, got %q", article.Content)
	}
	if len(article.Attachments) != 1 || article.Attachments[0].ExternalURL == "" {
		t.Fatalf("expected article reference attachment, got %+v", article.Attachments)
	}
	if article.Metadata["extractor"] != "linkcontent-local" {
		t.Fatalf("expected linkcontent-local extractor metadata, got %+v", article.Metadata)
	}
	pdf := fragments[1]
	if pdf.SourceType != "pdf" {
		t.Fatalf("expected pdf source type, got %s", pdf.SourceType)
	}
	if !strings.Contains(pdf.Content, "This is a heading") {
		t.Fatalf("expected extracted pdf content, got %q", pdf.Content)
	}
	if pdf.Attachments[0].Kind != "pdf" {
		t.Fatalf("expected pdf attachment kind, got %+v", pdf.Attachments[0])
	}
	if pdf.Metadata["extractor"] != "ledongthuc/pdf" {
		t.Fatalf("expected pdf extractor metadata, got %+v", pdf.Metadata)
	}
}

func TestSourceCollect_ArticleCapturesOGDescriptionSummary(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Fallback Title</title>
<meta property="og:title" content="Roadmap Planning Notes" />
<meta property="og:description" content="A short, deliberate summary of the roadmap planning session." />
</head><body><main><h1>Roadmap Planning Notes</h1><p>The roadmap starts with deterministic ingest and search, then expands from there.</p></main></body></html>`))
	}))
	defer srv.Close()

	root := t.TempDir()
	manifest := filepath.Join(root, "urls.txt")
	if err := os.WriteFile(manifest, []byte(srv.URL+"/article\n"), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	fragments, err := Source{now: func() time.Time { return time.Date(2026, 4, 27, 18, 0, 0, 0, time.UTC) }}.Collect(context.Background(), config.IngestConfig{
		Name:   "urls-og-test",
		Kind:   kind,
		Source: config.IngestSource{Root: root},
		Routing: config.IngestRouting{
			Namespace: "fragments/web",
		},
	})
	if err != nil {
		t.Fatalf("collect urls: %v", err)
	}
	if len(fragments) != 1 {
		t.Fatalf("expected 1 fragment, got %d", len(fragments))
	}
	article := fragments[0]
	if article.Title != "Roadmap Planning Notes" {
		t.Fatalf("unexpected article title: %s", article.Title)
	}
	if article.Metadata["summary"] != "A short, deliberate summary of the roadmap planning session." {
		t.Fatalf("expected og:description-derived summary metadata, got %+v", article.Metadata)
	}
	if article.Metadata["og:description"] == "" {
		t.Fatalf("expected og:description metadata to be captured, got %+v", article.Metadata)
	}
	if article.Metadata["extractor"] != "linkcontent-local" {
		t.Fatalf("expected linkcontent-local extractor metadata, got %+v", article.Metadata)
	}
}

func TestSourceCollect_BlockedArticleContinuesBatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/blocked":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`<html><body><p>Forbidden</p></body></html>`))
		case "/ok":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<!doctype html><html><head><title>OK Page</title></head><body><main><h1>OK</h1><p>This page fetched successfully after the blocked one in the batch.</p></main></body></html>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	root := t.TempDir()
	manifest := filepath.Join(root, "urls.txt")
	if err := os.WriteFile(manifest, []byte(strings.Join([]string{
		srv.URL + "/blocked",
		srv.URL + "/ok",
	}, "\n")), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	fragments, err := Source{now: func() time.Time { return time.Date(2026, 4, 27, 18, 0, 0, 0, time.UTC) }}.Collect(context.Background(), config.IngestConfig{
		Name:   "urls-blocked-test",
		Kind:   kind,
		Source: config.IngestSource{Root: root},
		Routing: config.IngestRouting{
			Namespace: "fragments/web",
		},
	})
	if err != nil {
		t.Fatalf("expected batch to continue past a blocked url without error, got: %v", err)
	}
	if len(fragments) != 2 {
		t.Fatalf("expected batch to continue past blocked url and produce 2 fragments, got %d", len(fragments))
	}

	var blocked, ok *domain.PipelineFragment
	for i := range fragments {
		f := &fragments[i]
		switch {
		case strings.Contains(f.Metadata["source_url"].(string), "/blocked"):
			blocked = f
		case strings.Contains(f.Metadata["source_url"].(string), "/ok"):
			ok = f
		}
	}
	if blocked == nil || ok == nil {
		t.Fatalf("expected both blocked and ok fragments, got %+v", fragments)
	}
	if blocked.Metadata["enrichment_status"] != "pending" {
		t.Fatalf("expected blocked url to have enrichment_status=pending, got %+v", blocked.Metadata)
	}
	if blocked.Metadata["enrichment_blocked"] != true {
		t.Fatalf("expected blocked url to have enrichment_blocked=true, got %+v", blocked.Metadata)
	}
	if blocked.Metadata["extractor"] != nil {
		t.Fatalf("did not expect extractor metadata on a pending/blocked fragment, got %+v", blocked.Metadata)
	}
	if !strings.Contains(blocked.Content, "pending") {
		t.Fatalf("expected placeholder pending content, got %q", blocked.Content)
	}

	if ok.Metadata["enrichment_status"] != nil {
		t.Fatalf("did not expect enrichment_status on a successful fragment, got %+v", ok.Metadata)
	}
	if ok.Title != "OK Page" {
		t.Fatalf("unexpected ok fragment title: %s", ok.Title)
	}
	if !strings.Contains(ok.Content, "fetched successfully") {
		t.Fatalf("expected ok fragment to have extracted content, got %q", ok.Content)
	}
}

func TestSourceCollect_JSONManifest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("Plain text body"))
	}))
	defer srv.Close()

	root := t.TempDir()
	raw := `[
		{"url": "` + srv.URL + `/note.txt", "title": "Saved note", "created_at": "2026-04-20T10:00:00Z", "labels": ["reading"]},
		"` + srv.URL + `/note.txt"
	]`
	if err := os.WriteFile(filepath.Join(root, "urls.json"), []byte(raw), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	fragments, err := Source{}.Collect(context.Background(), config.IngestConfig{
		Name:   "urls-json",
		Kind:   kind,
		Source: config.IngestSource{Root: root},
	})
	if err != nil {
		t.Fatalf("collect json manifest: %v", err)
	}
	if len(fragments) != 1 {
		t.Fatalf("expected deduped fragment count 1, got %d", len(fragments))
	}
	if fragments[0].Title != "Saved note" {
		t.Fatalf("unexpected manifest title: %s", fragments[0].Title)
	}
	if fragments[0].Metadata["source_file"] == "" {
		t.Fatalf("expected source_file metadata, got %+v", fragments[0].Metadata)
	}
}

func TestSourceCollect_YouTube(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/watch"):
			_, _ = w.Write([]byte(`<!doctype html><html><body>
"title":"Roadmap Video"
"shortDescription":"Video description"
"captionTracks":[{"baseUrl":"` + srv.URL + `/captions.xml"}]
</body></html>`))
		case strings.HasPrefix(r.URL.Path, "/oembed"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"title":"Roadmap Video","author_name":"Hollis"}`))
		case strings.HasPrefix(r.URL.Path, "/captions.xml"):
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<transcript><text start="0" dur="2">Hello world</text><text start="2" dur="2">Roadmap plans</text></transcript>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	restore := extract.SetYouTubeBaseURLsForTest(srv.URL+"/watch?v=%s", srv.URL+"/oembed?v=%s")
	defer restore()

	root := t.TempDir()
	manifest := filepath.Join(root, "urls.txt")
	if err := os.WriteFile(manifest, []byte("https://www.youtube.com/watch?v=abc123\n"), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	fragments, err := Source{now: func() time.Time { return time.Date(2026, 4, 27, 18, 0, 0, 0, time.UTC) }}.Collect(context.Background(), config.IngestConfig{
		Name:   "youtube-test",
		Kind:   kind,
		Source: config.IngestSource{Root: root},
		Routing: config.IngestRouting{
			Namespace: "fragments/web",
		},
		Rules: map[string]any{
			"request_timeout_seconds": 10,
			"max_body_mb":             1,
		},
	})
	if err != nil {
		t.Fatalf("collect youtube url: %v", err)
	}
	if len(fragments) != 1 {
		t.Fatalf("expected 1 fragment, got %d", len(fragments))
	}
	item := fragments[0]
	if item.SourceType != "youtube" {
		t.Fatalf("expected youtube source type, got %s", item.SourceType)
	}
	if !strings.Contains(item.Content, "Hello world") || !strings.Contains(item.Content, "Video description") {
		t.Fatalf("unexpected youtube content: %q", item.Content)
	}
	if item.Metadata["transcript_source"] != "watch_caption_track" {
		t.Fatalf("unexpected youtube metadata: %+v", item.Metadata)
	}
	if item.Attachments[0].Kind != "video" {
		t.Fatalf("expected video attachment kind, got %+v", item.Attachments[0])
	}
}

func TestSourceCollect_GenericVideoTranscript(t *testing.T) {
	scriptPath := filepath.Join(t.TempDir(), "yt-dlp")
	script := `#!/bin/sh
out=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--output" ]; then
    shift
    out="$1"
  elif [ "$1" = "--print" ]; then
    shift
  elif [ "$1" = "%(title)s" ]; then
    shift
    continue
  fi
  shift
done
dir=$(dirname "$out")
echo 'Vimeo Roadmap Review'
printf 'WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nhello from vimeo captions\n' > "$dir/video.en.vtt"
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write yt-dlp script: %v", err)
	}
	restoreYTDLP := extract.SetYTDLPCommandForTest(scriptPath)
	defer restoreYTDLP()

	root := t.TempDir()
	manifest := filepath.Join(root, "urls.txt")
	if err := os.WriteFile(manifest, []byte("https://vimeo.com/123456\n"), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	fragments, err := Source{now: func() time.Time { return time.Date(2026, 4, 27, 18, 0, 0, 0, time.UTC) }}.Collect(context.Background(), config.IngestConfig{
		Name:   "video-test",
		Kind:   kind,
		Source: config.IngestSource{Root: root},
		Routing: config.IngestRouting{
			Namespace: "fragments/web",
		},
	})
	if err != nil {
		t.Fatalf("collect generic video url: %v", err)
	}
	if len(fragments) != 1 {
		t.Fatalf("expected 1 fragment, got %d", len(fragments))
	}
	item := fragments[0]
	if item.SourceType != "video" {
		t.Fatalf("expected video source type, got %s", item.SourceType)
	}
	if item.Title != "Vimeo Roadmap Review" {
		t.Fatalf("unexpected video title: %s", item.Title)
	}
	if !strings.Contains(item.Content, "hello from vimeo captions") {
		t.Fatalf("unexpected video content: %q", item.Content)
	}
	if item.Metadata["transcript_source"] != "yt-dlp" {
		t.Fatalf("unexpected video metadata: %+v", item.Metadata)
	}
	if item.Attachments[0].Kind != "video" {
		t.Fatalf("expected video attachment kind, got %+v", item.Attachments[0])
	}
}
