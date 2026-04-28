package extract

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	ytDLPCommand       = "yt-dlp"
	youtubeWatchBase   = "https://www.youtube.com/watch?v=%s"
	youtubeOEmbedBase  = "https://www.youtube.com/oembed?url=https://www.youtube.com/watch?v=%s&format=json"
	youtubeTimedtextRe = regexp.MustCompile(`"captionTracks":\[(.*?)\]`)
	youtubeBaseURLRe   = regexp.MustCompile(`"baseUrl":"(.*?)"`)
)

func SetYouTubeBaseURLsForTest(watchBase, oembedBase string) func() {
	oldWatch := youtubeWatchBase
	oldOEmbed := youtubeOEmbedBase
	youtubeWatchBase = watchBase
	youtubeOEmbedBase = oembedBase
	return func() {
		youtubeWatchBase = oldWatch
		youtubeOEmbedBase = oldOEmbed
	}
}

func SetYTDLPCommandForTest(command string) func() {
	old := ytDLPCommand
	ytDLPCommand = command
	return func() {
		ytDLPCommand = old
	}
}

type YouTubeTranscript struct {
	Title    string
	Text     string
	Metadata map[string]any
}

func ExtractYouTubeTranscript(ctx context.Context, client *http.Client, rawURL, userAgent string) (YouTubeTranscript, error) {
	videoID := ExtractYouTubeID(rawURL)
	if videoID == "" {
		return YouTubeTranscript{}, fmt.Errorf("unable to extract youtube video id from %s", rawURL)
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}

	title, author, err := fetchYouTubeOEmbed(ctx, client, videoID, userAgent)
	if err != nil {
		title = ""
		author = ""
	}
	pageTitle, description, transcript, transcriptSource, pageErr := fetchYouTubeWatchTranscript(ctx, client, videoID, userAgent)
	if pageErr == nil {
		if title == "" {
			title = pageTitle
		}
		text := strings.TrimSpace(strings.Join([]string{
			strings.TrimSpace(description),
			strings.TrimSpace(transcript),
		}, "\n\n"))
		if text != "" {
			return YouTubeTranscript{
				Title: titleOrFallback(title, rawURL),
				Text:  text,
				Metadata: map[string]any{
					"video_id":          videoID,
					"video_platform":    "youtube",
					"author":            author,
					"transcript_source": transcriptSource,
				},
			}, nil
		}
	}

	fallback, fallbackErr := extractYouTubeTranscriptWithYTDLP(ctx, rawURL)
	if fallbackErr == nil && strings.TrimSpace(fallback.Text) != "" {
		fallback.Metadata["video_id"] = videoID
		fallback.Metadata["video_platform"] = "youtube"
		if title != "" {
			fallback.Title = titleOrFallback(title, rawURL)
		}
		if author != "" {
			fallback.Metadata["author"] = author
		}
		if pageErr != nil {
			fallback.Metadata["watch_page_error"] = pageErr.Error()
		}
		return fallback, nil
	}

	errs := []string{}
	if pageErr != nil {
		errs = append(errs, pageErr.Error())
	}
	if fallbackErr != nil {
		errs = append(errs, fallbackErr.Error())
	}
	if len(errs) == 0 {
		errs = append(errs, "transcript unavailable")
	}
	return YouTubeTranscript{
		Title: titleOrFallback(title, rawURL),
		Text:  "[Transcript unavailable for YouTube video " + videoID + "]",
		Metadata: map[string]any{
			"video_id":       videoID,
			"video_platform": "youtube",
			"author":         author,
			"errors":         errs,
		},
	}, nil
}

func ExtractYouTubeID(rawURL string) string {
	u, err := neturl.Parse(rawURL)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Host)
	switch {
	case strings.Contains(host, "youtu.be"):
		return strings.Trim(strings.TrimPrefix(u.Path, "/"), "/")
	case strings.Contains(host, "youtube.com"):
		if id := strings.TrimSpace(u.Query().Get("v")); id != "" {
			return id
		}
	}
	return ""
}

func IsYouTubeURL(rawURL string) bool {
	u, err := neturl.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Host)
	return strings.Contains(host, "youtube.com") || strings.Contains(host, "youtu.be")
}

func fetchYouTubeOEmbed(ctx context.Context, client *http.Client, videoID, userAgent string) (title, author string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf(youtubeOEmbedBase, videoID), nil)
	if err != nil {
		return "", "", err
	}
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("youtube oembed status %d", resp.StatusCode)
	}
	var data struct {
		Title      string `json:"title"`
		AuthorName string `json:"author_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", "", err
	}
	return strings.TrimSpace(data.Title), strings.TrimSpace(data.AuthorName), nil
}

func fetchYouTubeWatchTranscript(ctx context.Context, client *http.Client, videoID, userAgent string) (title, description, transcript, source string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf(youtubeWatchBase, videoID), nil)
	if err != nil {
		return "", "", "", "", err
	}
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", "", "", fmt.Errorf("youtube watch status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", "", "", err
	}
	html := string(body)
	title = strings.TrimSpace(extractJSONField(html, `"title":"`, `"`))
	description = strings.TrimSpace(unescapeYouTubeString(extractJSONField(html, `"shortDescription":"`, `"`)))
	captionURL := extractCaptionTrackURL(html)
	if captionURL == "" {
		return title, description, "", "", fmt.Errorf("no youtube caption track found")
	}
	req2, err := http.NewRequestWithContext(ctx, http.MethodGet, captionURL, nil)
	if err != nil {
		return title, description, "", "", err
	}
	if userAgent != "" {
		req2.Header.Set("User-Agent", userAgent)
	}
	resp2, err := client.Do(req2)
	if err != nil {
		return title, description, "", "", err
	}
	defer resp2.Body.Close()
	if resp2.StatusCode < 200 || resp2.StatusCode >= 300 {
		return title, description, "", "", fmt.Errorf("youtube caption status %d", resp2.StatusCode)
	}
	captionBody, err := io.ReadAll(resp2.Body)
	if err != nil {
		return title, description, "", "", err
	}
	transcript = parseTimedTextXML(captionBody)
	if strings.TrimSpace(transcript) == "" {
		return title, description, "", "", fmt.Errorf("youtube transcript empty")
	}
	return title, description, transcript, "watch_caption_track", nil
}

func extractCaptionTrackURL(html string) string {
	match := youtubeTimedtextRe.FindStringSubmatch(html)
	if len(match) < 2 {
		return ""
	}
	base := youtubeBaseURLRe.FindStringSubmatch(match[1])
	if len(base) < 2 {
		return ""
	}
	return unescapeYouTubeString(base[1])
}

func extractJSONField(body, prefix, suffix string) string {
	idx := strings.Index(body, prefix)
	if idx == -1 {
		return ""
	}
	rest := body[idx+len(prefix):]
	end := strings.Index(rest, suffix)
	if end == -1 {
		return ""
	}
	return rest[:end]
}

func unescapeYouTubeString(in string) string {
	in = strings.ReplaceAll(in, `\u0026`, "&")
	in = strings.ReplaceAll(in, `\/`, "/")
	in = strings.ReplaceAll(in, `\"`, `"`)
	in = strings.ReplaceAll(in, `\n`, "\n")
	return in
}

type timedTextTranscript struct {
	Text []struct {
		Body string `xml:",chardata"`
	} `xml:"text"`
}

func parseTimedTextXML(body []byte) string {
	var doc timedTextTranscript
	if err := xml.Unmarshal(body, &doc); err != nil {
		return ""
	}
	parts := make([]string, 0, len(doc.Text))
	for _, item := range doc.Text {
		text := strings.TrimSpace(item.Body)
		if text == "" {
			continue
		}
		text = strings.ReplaceAll(text, "&#39;", "'")
		text = strings.ReplaceAll(text, "&quot;", `"`)
		text = strings.ReplaceAll(text, "&amp;", "&")
		text = strings.ReplaceAll(text, "&lt;", "<")
		text = strings.ReplaceAll(text, "&gt;", ">")
		parts = append(parts, text)
	}
	return strings.Join(parts, " ")
}

func extractYouTubeTranscriptWithYTDLP(ctx context.Context, rawURL string) (YouTubeTranscript, error) {
	path, err := exec.LookPath(ytDLPCommand)
	if err != nil {
		return YouTubeTranscript{}, fmt.Errorf("yt-dlp unavailable: %w", err)
	}
	tempDir, err := os.MkdirTemp("", "fe-ytdlp-*")
	if err != nil {
		return YouTubeTranscript{}, fmt.Errorf("create yt-dlp temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	cmdCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, path,
		"--skip-download",
		"--write-auto-sub",
		"--write-sub",
		"--sub-lang", "en.*",
		"--sub-format", "vtt",
		"--output", filepath.Join(tempDir, "%(id)s.%(ext)s"),
		rawURL,
	)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return YouTubeTranscript{}, fmt.Errorf("run yt-dlp: %s", msg)
	}
	matches, err := filepath.Glob(filepath.Join(tempDir, "*.vtt"))
	if err != nil || len(matches) == 0 {
		return YouTubeTranscript{}, fmt.Errorf("yt-dlp produced no subtitle files")
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		return YouTubeTranscript{}, err
	}
	text := parseVTT(raw)
	if strings.TrimSpace(text) == "" {
		return YouTubeTranscript{}, fmt.Errorf("yt-dlp subtitle text empty")
	}
	return YouTubeTranscript{
		Text: text,
		Metadata: map[string]any{
			"transcript_source": "yt-dlp",
			"subtitle_file":     filepath.Base(matches[0]),
		},
	}, nil
}

func parseVTT(body []byte) string {
	lines := strings.Split(string(body), "\n")
	var parts []string
	timestampRe := regexp.MustCompile(`^\d{2}:\d{2}:\d{2}\.\d{3}\s+-->`)
	indexRe := regexp.MustCompile(`^\d+$`)
	tagRe := regexp.MustCompile(`</?[^>]+>`)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		switch {
		case line == "", line == "WEBVTT", strings.HasPrefix(line, "Kind:"), strings.HasPrefix(line, "Language:"):
			continue
		case timestampRe.MatchString(line), indexRe.MatchString(line):
			continue
		default:
			line = tagRe.ReplaceAllString(line, "")
			line = strings.TrimSpace(line)
			if line != "" {
				parts = append(parts, line)
			}
		}
	}
	return strings.Join(parts, " ")
}

func titleOrFallback(title, rawURL string) string {
	title = strings.TrimSpace(title)
	if title != "" {
		return title
	}
	return rawURL
}
