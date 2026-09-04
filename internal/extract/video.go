package extract

import (
	"bytes"
	"context"
	"fmt"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var knownVideoHosts = []string{
	"vimeo.com",
	"loom.com",
	"wistia.com",
	"dailymotion.com",
	"twitch.tv",
	"tiktok.com",
}

func IsVideoTranscriptCandidateURL(rawURL string) bool {
	u, err := neturl.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Host)
	if strings.Contains(host, "youtube.com") || strings.Contains(host, "youtu.be") {
		return false
	}
	for _, candidate := range knownVideoHosts {
		if strings.Contains(host, candidate) {
			return true
		}
	}
	lowerPath := strings.ToLower(u.Path)
	return hasAnySuffix(lowerPath, ".mp4", ".mov", ".webm", ".mkv", ".m4v")
}

func ExtractVideoTranscript(ctx context.Context, rawURL string) (ExtractedContent, error) {
	path, err := exec.LookPath(ytDLPCommand)
	if err != nil {
		return ExtractedContent{}, fmt.Errorf("yt-dlp unavailable: %w", err)
	}
	tempDir, err := os.MkdirTemp("", "fe-video-ytdlp-*")
	if err != nil {
		return ExtractedContent{}, fmt.Errorf("create yt-dlp temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	cmdCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, path,
		"--skip-download",
		"--write-auto-sub",
		"--write-sub",
		"--sub-lang", "en.*,en",
		"--sub-format", "vtt",
		"--convert-subs", "vtt",
		"--print", "%(title)s",
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
		return ExtractedContent{}, fmt.Errorf("run yt-dlp: %s", msg)
	}

	matches, err := filepath.Glob(filepath.Join(tempDir, "*.vtt"))
	if err != nil || len(matches) == 0 {
		return ExtractedContent{}, fmt.Errorf("yt-dlp produced no subtitle files")
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		return ExtractedContent{}, err
	}
	text := strings.TrimSpace(parseVTT(raw))
	if text == "" {
		return ExtractedContent{}, fmt.Errorf("yt-dlp subtitle text empty")
	}
	title := firstNonEmptyLine(stdout.String())
	return ExtractedContent{
		Title: title,
		Text:  text,
		Metadata: map[string]any{
			"extractor":         "yt-dlp",
			"transcript_source": "yt-dlp",
			"transcript_text":   text,
			"subtitle_file":     filepath.Base(matches[0]),
		},
	}, nil
}

func firstNonEmptyLine(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

func hasAnySuffix(s string, suffixes ...string) bool {
	for _, suffix := range suffixes {
		if strings.HasSuffix(s, suffix) {
			return true
		}
	}
	return false
}
