package extract

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExtractArticle(t *testing.T) {
	body := []byte(`<!doctype html><html><head><title>Roadmap Review</title></head><body><header><nav>menu menu menu</nav></header><article><h1>Roadmap</h1><p>The roadmap starts with deterministic ingest and search.</p><p>Later it expands into recall and routing.</p></article><footer>footer links</footer></body></html>`)
	out, err := ExtractArticle(body, "https://example.com/article")
	if err != nil {
		t.Fatalf("extract article: %v", err)
	}
	if out.Title != "Roadmap Review" {
		t.Fatalf("unexpected title: %s", out.Title)
	}
	if !strings.Contains(out.Text, "deterministic ingest and search") {
		t.Fatalf("expected extracted article text, got %q", out.Text)
	}
	if out.Metadata["page_type"] == "" {
		t.Fatalf("expected page_type metadata, got %+v", out.Metadata)
	}
}

func TestExtractPDF(t *testing.T) {
	body, err := os.ReadFile("../../testdata/urlsource-sample.pdf")
	if err != nil {
		t.Fatalf("read pdf fixture: %v", err)
	}
	out, err := ExtractPDF(body)
	if err != nil {
		t.Fatalf("extract pdf: %v", err)
	}
	if !strings.Contains(out.Text, "This is a heading") {
		t.Fatalf("expected extracted pdf text, got %q", out.Text)
	}
	if out.Metadata["page_count"] != 1 {
		t.Fatalf("expected page_count metadata, got %+v", out.Metadata)
	}
	if out.Metadata["text_page_count"] != 1 {
		t.Fatalf("expected text_page_count metadata, got %+v", out.Metadata)
	}
	pageSnippets, ok := out.Metadata["page_snippets"].([]map[string]any)
	if !ok || len(pageSnippets) != 1 {
		t.Fatalf("expected page_snippets metadata, got %+v", out.Metadata)
	}
	if pageSnippets[0]["page"] != 1 {
		t.Fatalf("expected first page snippet metadata, got %+v", pageSnippets[0])
	}
	if out.Metadata["extractor"] != "ledongthuc/pdf" {
		t.Fatalf("expected embedded extractor metadata, got %+v", out.Metadata)
	}
}

func TestExtractPDF_FallsBackToPDFToText(t *testing.T) {
	scriptPath := filepath.Join(t.TempDir(), "pdftotext")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\necho 'pdftotext fallback output'\n"), 0o700); err != nil {
		t.Fatalf("write pdftotext script: %v", err)
	}
	old := pdftotextCommand
	pdftotextCommand = scriptPath
	defer func() { pdftotextCommand = old }()

	out, err := ExtractPDF([]byte("not-a-real-pdf"))
	if err != nil {
		t.Fatalf("extract pdf with fallback: %v", err)
	}
	if out.Metadata["extractor"] != "pdftotext" {
		t.Fatalf("expected pdftotext fallback metadata, got %+v", out.Metadata)
	}
	if !strings.Contains(out.Text, "pdftotext fallback output") {
		t.Fatalf("expected fallback text, got %q", out.Text)
	}
	if out.Metadata["embedded_error"] == "" {
		t.Fatalf("expected embedded error metadata, got %+v", out.Metadata)
	}
}

func TestExtractPDF_FallsBackToOCRMyPDF(t *testing.T) {
	pdftotextPath := filepath.Join(t.TempDir(), "pdftotext")
	if err := os.WriteFile(pdftotextPath, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatalf("write pdftotext script: %v", err)
	}
	ocrPath := filepath.Join(t.TempDir(), "ocrmypdf")
	if err := os.WriteFile(ocrPath, []byte("#!/bin/sh\necho 'ocrmypdf fallback output'\n"), 0o700); err != nil {
		t.Fatalf("write ocrmypdf script: %v", err)
	}
	oldPDFToText := pdftotextCommand
	oldOCR := ocrmypdfCommand
	pdftotextCommand = pdftotextPath
	ocrmypdfCommand = ocrPath
	defer func() {
		pdftotextCommand = oldPDFToText
		ocrmypdfCommand = oldOCR
	}()

	out, err := ExtractPDF([]byte("not-a-real-pdf"))
	if err != nil {
		t.Fatalf("extract pdf with ocrmypdf fallback: %v", err)
	}
	if out.Metadata["extractor"] != "ocrmypdf" {
		t.Fatalf("expected ocrmypdf fallback metadata, got %+v", out.Metadata)
	}
	if !strings.Contains(out.Text, "ocrmypdf fallback output") {
		t.Fatalf("expected ocrmypdf fallback text, got %q", out.Text)
	}
	if out.Metadata["embedded_error"] == "" {
		t.Fatalf("expected embedded error metadata, got %+v", out.Metadata)
	}
	if out.Metadata["pdftotext_error"] == "" {
		t.Fatalf("expected pdftotext error metadata, got %+v", out.Metadata)
	}
}

func TestExtractYouTubeTranscript(t *testing.T) {
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
	restore := SetYouTubeBaseURLsForTest(srv.URL+"/watch?v=%s", srv.URL+"/oembed?v=%s")
	defer restore()

	out, err := ExtractYouTubeTranscript(context.Background(), srv.Client(), "https://www.youtube.com/watch?v=abc123", "FragmentsEngineTest/1.0")
	if err != nil {
		t.Fatalf("extract youtube transcript: %v", err)
	}
	if out.Title != "Roadmap Video" {
		t.Fatalf("unexpected youtube title: %s", out.Title)
	}
	if !strings.Contains(out.Text, "Hello world") || !strings.Contains(out.Text, "Video description") {
		t.Fatalf("unexpected youtube transcript text: %q", out.Text)
	}
	if out.Metadata["video_id"] != "abc123" {
		t.Fatalf("unexpected youtube metadata: %+v", out.Metadata)
	}
	if out.Metadata["transcript_source"] != "watch_caption_track" {
		t.Fatalf("unexpected transcript source metadata: %+v", out.Metadata)
	}
}

func TestExtractYouTubeTranscript_FallsBackToYTDLP(t *testing.T) {
	scriptPath := filepath.Join(t.TempDir(), "yt-dlp")
	script := `#!/bin/sh
out=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--output" ]; then
    shift
    out="$1"
  fi
  shift
done
dir=$(dirname "$out")
printf 'WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nhello from ytdlp\n' > "$dir/video.en.vtt"
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write yt-dlp script: %v", err)
	}
	restoreYTDLP := SetYTDLPCommandForTest(scriptPath)
	restore := SetYouTubeBaseURLsForTest("http://127.0.0.1:1/watch?v=%s", "http://127.0.0.1:1/oembed?v=%s")
	defer func() {
		restoreYTDLP()
		restore()
	}()

	out, err := ExtractYouTubeTranscript(context.Background(), &http.Client{Timeout: 100 * time.Millisecond}, "https://youtu.be/abc123", "FragmentsEngineTest/1.0")
	if err != nil {
		t.Fatalf("extract youtube transcript with yt-dlp fallback: %v", err)
	}
	if !strings.Contains(out.Text, "hello from ytdlp") {
		t.Fatalf("unexpected yt-dlp fallback text: %q", out.Text)
	}
	if out.Metadata["transcript_source"] != "yt-dlp" {
		t.Fatalf("unexpected yt-dlp metadata: %+v", out.Metadata)
	}
}

func TestExtractVideoTranscript(t *testing.T) {
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
echo 'Roadmap Session Video'
printf 'WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nhello from generic video\n' > "$dir/video.en.vtt"
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write yt-dlp script: %v", err)
	}
	restoreYTDLP := SetYTDLPCommandForTest(scriptPath)
	defer restoreYTDLP()

	out, err := ExtractVideoTranscript(context.Background(), "https://vimeo.com/123456")
	if err != nil {
		t.Fatalf("extract generic video transcript: %v", err)
	}
	if out.Title != "Roadmap Session Video" {
		t.Fatalf("unexpected generic video title: %s", out.Title)
	}
	if !strings.Contains(out.Text, "hello from generic video") {
		t.Fatalf("unexpected generic video text: %q", out.Text)
	}
	if out.Metadata["extractor"] != "yt-dlp" || out.Metadata["transcript_source"] != "yt-dlp" {
		t.Fatalf("unexpected generic video metadata: %+v", out.Metadata)
	}
}

func TestIsVideoTranscriptCandidateURL(t *testing.T) {
	cases := []struct {
		rawURL string
		want   bool
	}{
		{rawURL: "https://vimeo.com/123456", want: true},
		{rawURL: "https://loom.com/share/abc", want: true},
		{rawURL: "https://cdn.example.com/video.mp4", want: true},
		{rawURL: "https://www.youtube.com/watch?v=abc123", want: false},
		{rawURL: "https://example.com/article", want: false},
	}
	for _, tc := range cases {
		if got := IsVideoTranscriptCandidateURL(tc.rawURL); got != tc.want {
			t.Fatalf("candidate(%s) = %v, want %v", tc.rawURL, got, tc.want)
		}
	}
}
