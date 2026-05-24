package filesystemdocs

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/ingest/sourceutil"
	"gopkg.in/yaml.v3"
)

const kind = "filesystem_docs"

type Source struct{}

func (Source) Kind() string {
	return kind
}

func (Source) Collect(ctx context.Context, cfg config.IngestConfig) ([]domain.PipelineFragment, error) {
	root := config.ExpandHome(cfg.Source.Root)
	rules, err := config.DecodeRules[config.FilesystemDocsRules](cfg)
	if err != nil {
		return nil, err
	}
	if err := sourceutil.ValidateGlobs(rules.Include); err != nil {
		return nil, err
	}
	if err := sourceutil.ValidateGlobs(rules.Exclude); err != nil {
		return nil, err
	}
	maxSizeBytes := int64(2 * 1024 * 1024)
	if rules.MaxFileSizeMB > 0 {
		maxSizeBytes = int64(rules.MaxFileSizeMB) * 1024 * 1024
	}

	var paths []string
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		include, err := sourceutil.ShouldIncludePath(rel, rules.Include, rules.Exclude)
		if err != nil {
			return err
		}
		if include {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk docs root: %w", err)
	}
	sort.Strings(paths)

	out := make([]domain.PipelineFragment, 0, len(paths))
	for _, path := range paths {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", path, err)
		}
		if info.Size() > maxSizeBytes {
			continue
		}
		item, err := collectFile(root, path, cfg, rules, info)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(item.Content) == "" {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func collectFile(root, path string, cfg config.IngestConfig, rules config.FilesystemDocsRules, info os.FileInfo) (domain.PipelineFragment, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return domain.PipelineFragment{}, fmt.Errorf("read doc file %s: %w", path, err)
	}
	if len(raw) == 0 {
		return domain.PipelineFragment{}, nil
	}
	relRoot, err := filepath.Rel(root, path)
	if err != nil {
		return domain.PipelineFragment{}, fmt.Errorf("relative path %s: %w", path, err)
	}
	repoRoot, repoName, repoRel := repoContext(root, path)
	frontmatter, body := parseFrontmatter(string(raw))
	content := normalizeBody(body)
	if strings.TrimSpace(content) == "" {
		return domain.PipelineFragment{}, nil
	}
	title := documentTitle(path, frontmatter, content)
	project := projectName(root, relRoot, repoName, rules.ProjectFromPath)
	metadata := map[string]any{
		"project":       project,
		"repo_name":     repoName,
		"repo_root":     repoRoot,
		"relative_path": sourceutil.NormalizePath(repoRel),
		"file_ext":      strings.ToLower(filepath.Ext(path)),
		"content_hash":  sourceutil.HashText(content),
		"ingest_mode":   "docs",
		"source_path":   path,
	}
	if len(frontmatter) > 0 {
		metadata["frontmatter"] = frontmatter
	}
	canonicalPath := canonicalDocPath(cfg, project, repoName, repoRel)
	return domain.PipelineFragment{
		Source:        sourceutil.FileURI(path),
		SourceType:    kind,
		SourceID:      path,
		Title:         title,
		Content:       content,
		CreatedAt:     info.ModTime().UTC(),
		Metadata:      metadata,
		CanonicalPath: canonicalPath,
	}, nil
}

func canonicalDocPath(cfg config.IngestConfig, project, repoName, repoRel string) string {
	base := strings.Trim(sourceutil.NormalizePath(cfg.Routing.Namespace), "/")
	if base == "" {
		base = "fragments/repos/docs"
	}
	scope := strings.Trim(repoName, "/")
	if scope == "" {
		scope = strings.Trim(project, "/")
	}
	if scope == "" {
		scope = "docs"
	}
	rel := strings.Trim(sourceutil.NormalizePath(repoRel), "/")
	if rel == "" {
		return strings.Trim(base+"/"+scope, "/")
	}
	return strings.Trim(base+"/"+scope+"/"+rel, "/")
}

func repoContext(root, path string) (repoRoot, repoName, repoRel string) {
	dir := filepath.Dir(path)
	for {
		if dir == "" || dir == "." || dir == string(filepath.Separator) {
			break
		}
		if info, err := os.Stat(filepath.Join(dir, ".git")); err == nil && info.IsDir() {
			repoRoot = dir
			repoName = filepath.Base(dir)
			if rel, err := filepath.Rel(dir, path); err == nil {
				repoRel = rel
			}
			return repoRoot, repoName, repoRel
		}
		if filepath.Clean(dir) == filepath.Clean(root) {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if rel, err := filepath.Rel(root, path); err == nil {
		repoRel = rel
	}
	return "", "", repoRel
}

func projectName(root, relRoot, repoName string, projectFromPath bool) string {
	if strings.TrimSpace(repoName) != "" {
		return repoName
	}
	if !projectFromPath {
		return filepath.Base(root)
	}
	parts := strings.Split(sourceutil.NormalizePath(relRoot), "/")
	if len(parts) > 1 && strings.TrimSpace(parts[0]) != "" {
		return parts[0]
	}
	return filepath.Base(root)
}

func documentTitle(path string, frontmatter map[string]any, content string) string {
	if rawTitle, ok := frontmatter["title"].(string); ok && strings.TrimSpace(rawTitle) != "" {
		return strings.TrimSpace(rawTitle)
	}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			return strings.TrimSpace(strings.TrimLeft(line, "#"))
		}
		if line != "" {
			break
		}
	}
	return filepath.Base(path)
}

func normalizeBody(body string) string {
	return strings.TrimSpace(strings.ReplaceAll(body, "\r\n", "\n"))
}

func parseFrontmatter(body string) (map[string]any, string) {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	if !strings.HasPrefix(body, "---\n") {
		return nil, body
	}
	rest := strings.TrimPrefix(body, "---\n")
	endIdx := strings.Index(rest, "\n---\n")
	tokenLen := len("\n---\n")
	if endIdx == -1 {
		endIdx = strings.Index(rest, "\n...\n")
		tokenLen = len("\n...\n")
	}
	if endIdx == -1 {
		return nil, body
	}
	var frontmatter map[string]any
	if err := yaml.NewDecoder(bytes.NewBufferString(rest[:endIdx])).Decode(&frontmatter); err != nil || len(frontmatter) == 0 {
		return nil, body
	}
	return frontmatter, rest[endIdx+tokenLen:]
}
