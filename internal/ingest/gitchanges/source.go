package gitchanges

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/ingest/sourceutil"
)

const kind = "git_changes"

type Source struct {
	now func() time.Time
}

func (Source) Kind() string {
	return kind
}

type repoCommit struct {
	RepoName    string
	RepoRoot    string
	Branch      string
	SHA         string
	Author      string
	AuthorEmail string
	CreatedAt   time.Time
	Subject     string
	Body        string
	Files       []commitFile
	DiffStat    string
}

type commitFile struct {
	Path      string
	Status    string
	Additions int
	Deletions int
}

func (s Source) Collect(ctx context.Context, cfg config.IngestConfig) ([]domain.PipelineFragment, error) {
	root := config.ExpandHome(cfg.Source.Root)
	rules, err := config.DecodeRules[config.GitChangesRules](cfg)
	if err != nil {
		return nil, err
	}
	if err := sourceutil.ValidateGlobs(rules.Include); err != nil {
		return nil, err
	}
	if err := sourceutil.ValidateGlobs(rules.Exclude); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if s.now != nil {
		now = s.now().UTC()
	}
	repos, err := resolveRepos(root, rules.Repos)
	if err != nil {
		return nil, err
	}
	out := make([]domain.PipelineFragment, 0)
	for _, repoRoot := range repos {
		branch, err := branchForRepo(ctx, repoRoot, rules.Branch)
		if err != nil {
			return nil, err
		}
		commits, err := collectRepoCommits(ctx, repoRoot, branch, rules, now)
		if err != nil {
			return nil, err
		}
		for _, commit := range commits {
			out = append(out, commitFragment(cfg, commit))
			if !rules.EmitDocFileFragments {
				continue
			}
			for _, file := range commit.Files {
				item, ok, err := changedFileFragment(ctx, cfg, commit, file)
				if err != nil {
					return nil, err
				}
				if ok {
					out = append(out, item)
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].SourceID < out[j].SourceID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func resolveRepos(root string, repos []string) ([]string, error) {
	if len(repos) == 0 {
		return []string{root}, nil
	}
	out := make([]string, 0, len(repos))
	for _, repo := range repos {
		repo = strings.TrimSpace(repo)
		if repo == "" {
			continue
		}
		path := repo
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, repo)
		}
		out = append(out, path)
	}
	sort.Strings(out)
	return out, nil
}

func branchForRepo(ctx context.Context, repoRoot, configured string) (string, error) {
	if strings.TrimSpace(configured) != "" {
		return strings.TrimSpace(configured), nil
	}
	out, err := gitOutput(ctx, repoRoot, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func collectRepoCommits(ctx context.Context, repoRoot, branch string, rules config.GitChangesRules, now time.Time) ([]repoCommit, error) {
	shas, err := listCommitSHAs(ctx, repoRoot, branch, rules, now)
	if err != nil {
		return nil, err
	}
	commits := make([]repoCommit, 0, len(shas))
	for _, sha := range shas {
		commit, ok, err := inspectCommit(ctx, repoRoot, branch, sha, rules)
		if err != nil {
			return nil, err
		}
		if ok {
			commits = append(commits, commit)
		}
	}
	return commits, nil
}

func listCommitSHAs(ctx context.Context, repoRoot, branch string, rules config.GitChangesRules, now time.Time) ([]string, error) {
	args := []string{"log", "--format=%H"}
	if strings.TrimSpace(branch) != "" {
		args = append(args, branch)
	}
	if since, err := sourceutil.ParseTimeBound(now, rules.Since, true); err != nil {
		return nil, err
	} else if since != "" {
		args = append(args, "--since="+since)
	}
	if until, err := sourceutil.ParseTimeBound(now, rules.Until, false); err != nil {
		return nil, err
	} else if until != "" {
		args = append(args, "--until="+until)
	}
	if rules.MaxCommits > 0 {
		args = append(args, fmt.Sprintf("--max-count=%d", rules.MaxCommits))
	}
	out, err := gitOutput(ctx, repoRoot, args...)
	if err != nil {
		return nil, err
	}
	lines := strings.Fields(out)
	return lines, nil
}

func inspectCommit(ctx context.Context, repoRoot, branch, sha string, rules config.GitChangesRules) (repoCommit, bool, error) {
	metaOut, err := gitOutput(ctx, repoRoot, "show", "-s", "--format=%H%x1f%an%x1f%ae%x1f%aI%x1f%s%x1f%b", sha)
	if err != nil {
		return repoCommit{}, false, err
	}
	parts := strings.SplitN(metaOut, "\x1f", 6)
	if len(parts) != 6 {
		return repoCommit{}, false, fmt.Errorf("unexpected git show metadata for %s", sha)
	}
	createdAt, err := time.Parse(time.RFC3339, strings.TrimSpace(parts[3]))
	if err != nil {
		return repoCommit{}, false, fmt.Errorf("parse commit time %s: %w", sha, err)
	}
	statusOut, err := gitOutput(ctx, repoRoot, "diff-tree", "--root", "--no-commit-id", "--name-status", "-r", sha)
	if err != nil {
		return repoCommit{}, false, err
	}
	numstatOut, err := gitOutput(ctx, repoRoot, "show", "--numstat", "--format=", sha)
	if err != nil {
		return repoCommit{}, false, err
	}
	files, err := parseCommitFiles(statusOut, numstatOut, rules.Include, rules.Exclude)
	if err != nil {
		return repoCommit{}, false, err
	}
	if len(files) == 0 {
		return repoCommit{}, false, nil
	}
	diffStat, err := diffStatSummary(ctx, repoRoot, sha)
	if err != nil {
		return repoCommit{}, false, err
	}
	return repoCommit{
		RepoName:    filepath.Base(repoRoot),
		RepoRoot:    repoRoot,
		Branch:      branch,
		SHA:         sha,
		Author:      strings.TrimSpace(parts[1]),
		AuthorEmail: strings.TrimSpace(parts[2]),
		CreatedAt:   createdAt.UTC(),
		Subject:     strings.TrimSpace(parts[4]),
		Body:        strings.TrimSpace(parts[5]),
		Files:       files,
		DiffStat:    diffStat,
	}, true, nil
}

func parseCommitFiles(statusOut, numstatOut string, include, exclude []string) ([]commitFile, error) {
	files := map[string]*commitFile{}
	for _, line := range strings.Split(strings.TrimSpace(statusOut), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			continue
		}
		path := fields[len(fields)-1]
		includeFile, err := sourceutil.ShouldIncludePath(path, include, exclude)
		if err != nil {
			return nil, err
		}
		if !includeFile {
			continue
		}
		files[path] = &commitFile{
			Path:   sourceutil.NormalizePath(path),
			Status: strings.TrimSpace(fields[0]),
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(numstatOut), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 3 {
			continue
		}
		item, ok := files[fields[2]]
		if !ok {
			continue
		}
		item.Additions = parseNumstatValue(fields[0])
		item.Deletions = parseNumstatValue(fields[1])
	}
	out := make([]commitFile, 0, len(files))
	for _, path := range sourceutil.SortedKeys(files) {
		out = append(out, *files[path])
	}
	return out, nil
}

func parseNumstatValue(raw string) int {
	if raw == "-" {
		return 0
	}
	value, _ := strconv.Atoi(raw)
	return value
}

func diffStatSummary(ctx context.Context, repoRoot, sha string) (string, error) {
	out, err := gitOutput(ctx, repoRoot, "show", "--stat", "--format=", sha)
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			return line, nil
		}
	}
	return "", nil
}

func commitFragment(cfg config.IngestConfig, commit repoCommit) domain.PipelineFragment {
	title := commit.Subject
	if title == "" {
		title = commit.SHA
	}
	lines := make([]string, 0, len(commit.Files))
	metadataFiles := make([]map[string]any, 0, len(commit.Files))
	for _, file := range commit.Files {
		lines = append(lines, fmt.Sprintf("- %s %s (+%d -%d)", file.Status, file.Path, file.Additions, file.Deletions))
		metadataFiles = append(metadataFiles, map[string]any{
			"path":      file.Path,
			"status":    file.Status,
			"additions": file.Additions,
			"deletions": file.Deletions,
		})
	}
	content := fmt.Sprintf("# %s\n\nRepo: %s\nCommit: %s\nAuthor: %s\nTime: %s\n\n## Message\n\n%s\n\n## Changed Files\n\n%s",
		title,
		commit.RepoName,
		commit.SHA,
		commit.Author,
		commit.CreatedAt.Format(time.RFC3339),
		strings.TrimSpace(commit.Body),
		strings.Join(lines, "\n"),
	)
	content = strings.Replace(content, "\n\n## Message\n\n\n\n## Changed Files", "\n\n## Message\n\n(no commit body)\n\n## Changed Files", 1)
	return domain.PipelineFragment{
		Source:     fmt.Sprintf("git://%s/%s", commit.RepoName, commit.SHA),
		SourceType: kind,
		SourceID:   commit.SHA,
		Title:      title,
		Content:    content,
		CreatedAt:  commit.CreatedAt,
		Metadata: map[string]any{
			"project":           commit.RepoName,
			"repo_name":         commit.RepoName,
			"repo_root":         commit.RepoRoot,
			"git_branch":        commit.Branch,
			"git_commit":        commit.SHA,
			"git_author":        commit.Author,
			"git_author_email":  commit.AuthorEmail,
			"git_subject":       commit.Subject,
			"git_files_changed": metadataFiles,
			"git_diff_stat":     commit.DiffStat,
			"ingest_mode":       "commits",
		},
		CanonicalPath: canonicalCommitPath(cfg, commit),
	}
}

func changedFileFragment(ctx context.Context, cfg config.IngestConfig, commit repoCommit, file commitFile) (domain.PipelineFragment, bool, error) {
	if strings.HasPrefix(strings.ToUpper(file.Status), "D") {
		return domain.PipelineFragment{}, false, nil
	}
	if !sourceutil.IsDefaultDocPath(file.Path) {
		return domain.PipelineFragment{}, false, nil
	}
	body, err := gitOutput(ctx, commit.RepoRoot, "show", fmt.Sprintf("%s:%s", commit.SHA, file.Path))
	if err != nil {
		return domain.PipelineFragment{}, false, nil
	}
	content := strings.TrimSpace(strings.ReplaceAll(body, "\r\n", "\n"))
	if content == "" {
		return domain.PipelineFragment{}, false, nil
	}
	title := filepath.Base(file.Path) + " @ " + shortSHA(commit.SHA)
	return domain.PipelineFragment{
		Source:     fmt.Sprintf("git://%s/%s/%s", commit.RepoName, commit.SHA, file.Path),
		SourceType: kind,
		SourceID:   commit.SHA + ":" + file.Path,
		Title:      title,
		Content:    content,
		CreatedAt:  commit.CreatedAt,
		Metadata: map[string]any{
			"project":       commit.RepoName,
			"repo_name":     commit.RepoName,
			"repo_root":     commit.RepoRoot,
			"relative_path": file.Path,
			"file_ext":      strings.ToLower(filepath.Ext(file.Path)),
			"content_hash":  sourceutil.HashText(content),
			"git_branch":    commit.Branch,
			"git_commit":    commit.SHA,
			"git_author":    commit.Author,
			"git_subject":   commit.Subject,
			"git_status":    file.Status,
			"git_additions": file.Additions,
			"git_deletions": file.Deletions,
			"ingest_mode":   "changed_doc",
		},
		CanonicalPath: canonicalChangedFilePath(cfg, commit, file),
	}, true, nil
}

func canonicalCommitPath(cfg config.IngestConfig, commit repoCommit) string {
	base := strings.Trim(sourceutil.NormalizePath(cfg.Routing.Namespace), "/")
	if base == "" {
		base = "fragments/repos/git"
	}
	return strings.Trim(base+"/"+commit.RepoName+"/commits/"+commit.SHA, "/")
}

func canonicalChangedFilePath(cfg config.IngestConfig, commit repoCommit, file commitFile) string {
	base := strings.Trim(sourceutil.NormalizePath(cfg.Routing.Namespace), "/")
	if base == "" {
		base = "fragments/repos/git"
	}
	return strings.Trim(base+"/"+commit.RepoName+"/files/"+commit.SHA+"/"+sourceutil.NormalizePath(file.Path), "/")
}

func shortSHA(sha string) string {
	if len(sha) <= 12 {
		return sha
	}
	return sha[:12]
}

func gitOutput(ctx context.Context, repoRoot string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repoRoot}, args...)...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.String(), nil
}
