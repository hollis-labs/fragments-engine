package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	neturl "net/url"
	"strings"
	"time"
)

type StackExplorerClient struct {
	baseURL string
	client  *http.Client
}

type StackExplorerRepoRecord struct {
	ID          string
	Name        string
	URL         string
	Description string
	Stack       string
	Category    string
	Tags        []string
}

type StackExplorerSyncResult struct {
	RepoID string
	Status string
}

type StackExplorerScanResult struct {
	ScanID string
	Status string
}

type StackExplorerTagSyncResult struct {
	RepoID string
	Tags   []string
}

type stackExplorerImportRequest struct {
	Repos []stackExplorerImportRepo `json:"repos"`
}

type stackExplorerImportRepo struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	URL         string   `json:"url,omitempty"`
	Description string   `json:"description,omitempty"`
	Stack       string   `json:"stack,omitempty"`
	Category    string   `json:"category,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

func NewStackExplorerClient(baseURL string) *StackExplorerClient {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return nil
	}
	return &StackExplorerClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: 20 * time.Second},
	}
}

func (c *StackExplorerClient) UpsertRepo(ctx context.Context, repo StackExplorerRepoRecord) (StackExplorerSyncResult, error) {
	if c == nil {
		return StackExplorerSyncResult{}, fmt.Errorf("stack explorer client is not configured")
	}
	if strings.TrimSpace(repo.ID) == "" || strings.TrimSpace(repo.Name) == "" {
		return StackExplorerSyncResult{}, fmt.Errorf("stack explorer repo id and name are required")
	}
	getURL := c.apiURL("/repos/" + neturl.PathEscape(repo.ID) + "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, getURL, nil)
	if err != nil {
		return StackExplorerSyncResult{}, err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return StackExplorerSyncResult{}, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		if err := c.updateRepo(ctx, repo); err != nil {
			return StackExplorerSyncResult{}, err
		}
		return StackExplorerSyncResult{RepoID: repo.ID, Status: "updated"}, nil
	case http.StatusNotFound:
		if err := c.importRepo(ctx, repo); err != nil {
			return StackExplorerSyncResult{}, err
		}
		return StackExplorerSyncResult{RepoID: repo.ID, Status: "created"}, nil
	default:
		return StackExplorerSyncResult{}, fmt.Errorf("stack explorer repo probe status %d", resp.StatusCode)
	}
}

func (c *StackExplorerClient) EnsureScan(ctx context.Context, repoID, blueprint string) (StackExplorerScanResult, error) {
	if c == nil {
		return StackExplorerScanResult{}, fmt.Errorf("stack explorer client is not configured")
	}
	repoID = strings.TrimSpace(repoID)
	blueprint = strings.TrimSpace(blueprint)
	if repoID == "" || blueprint == "" {
		return StackExplorerScanResult{}, fmt.Errorf("stack explorer scan repo id and blueprint are required")
	}
	return c.createScan(ctx, repoID, blueprint)
}

func (c *StackExplorerClient) AddRepoTags(ctx context.Context, repoID string, tags []string) (StackExplorerTagSyncResult, error) {
	if c == nil {
		return StackExplorerTagSyncResult{}, fmt.Errorf("stack explorer client is not configured")
	}
	repoID = strings.TrimSpace(repoID)
	tags = dedupeStrings(tags)
	if repoID == "" || len(tags) == 0 {
		return StackExplorerTagSyncResult{}, fmt.Errorf("stack explorer repo id and tags are required")
	}
	body, err := json.Marshal(map[string]any{
		"tags": tags,
	})
	if err != nil {
		return StackExplorerTagSyncResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL("/repos/"+neturl.PathEscape(repoID)+"/tags"), bytes.NewReader(body))
	if err != nil {
		return StackExplorerTagSyncResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return StackExplorerTagSyncResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return StackExplorerTagSyncResult{}, fmt.Errorf("stack explorer repo tag sync status %d", resp.StatusCode)
	}
	var payload struct {
		Data struct {
			RepoID string   `json:"repo_id"`
			Tags   []string `json:"tags"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return StackExplorerTagSyncResult{}, err
	}
	return StackExplorerTagSyncResult{
		RepoID: firstNonEmpty(payload.Data.RepoID, repoID),
		Tags:   dedupeStrings(payload.Data.Tags),
	}, nil
}

func (c *StackExplorerClient) importRepo(ctx context.Context, repo StackExplorerRepoRecord) error {
	body, err := json.Marshal(stackExplorerImportRequest{
		Repos: []stackExplorerImportRepo{{
			ID:          repo.ID,
			Name:        repo.Name,
			URL:         repo.URL,
			Description: repo.Description,
			Stack:       repo.Stack,
			Category:    repo.Category,
			Tags:        dedupeStrings(repo.Tags),
		}},
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL("/repos/import"), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("stack explorer repo import status %d", resp.StatusCode)
	}
	return nil
}

func (c *StackExplorerClient) updateRepo(ctx context.Context, repo StackExplorerRepoRecord) error {
	body, err := json.Marshal(map[string]any{
		"id":          repo.ID,
		"name":        repo.Name,
		"url":         repo.URL,
		"description": repo.Description,
		"stack":       repo.Stack,
		"category":    repo.Category,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.apiURL("/repos/"+neturl.PathEscape(repo.ID)+"/"), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("stack explorer repo update status %d", resp.StatusCode)
	}
	return nil
}

func (c *StackExplorerClient) createScan(ctx context.Context, repoID, blueprint string) (StackExplorerScanResult, error) {
	body, err := json.Marshal(map[string]any{
		"repo_id":   repoID,
		"blueprint": blueprint,
	})
	if err != nil {
		return StackExplorerScanResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL("/scans/"), bytes.NewReader(body))
	if err != nil {
		return StackExplorerScanResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return StackExplorerScanResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return StackExplorerScanResult{}, fmt.Errorf("stack explorer scan create status %d", resp.StatusCode)
	}
	var payload struct {
		Data struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return StackExplorerScanResult{}, err
	}
	return StackExplorerScanResult{
		ScanID: payload.Data.ID,
		Status: firstNonEmpty(payload.Data.Status, "pending"),
	}, nil
}

func (c *StackExplorerClient) apiURL(path string) string {
	base := c.baseURL
	if !strings.HasSuffix(base, "/api") {
		base += "/api"
	}
	return base + path
}

func (c *StackExplorerClient) httpClient() *http.Client {
	if c.client != nil {
		return c.client
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func stackExplorerRepoID(owner, repo string) string {
	parts := []string{"github", owner, repo}
	var out []string
	for _, part := range parts {
		part = strings.ToLower(strings.TrimSpace(part))
		if part == "" {
			continue
		}
		part = strings.Map(func(r rune) rune {
			switch {
			case r >= 'a' && r <= 'z':
				return r
			case r >= '0' && r <= '9':
				return r
			default:
				return '-'
			}
		}, part)
		part = strings.Trim(part, "-")
		part = strings.Join(strings.FieldsFunc(part, func(r rune) bool { return r == '-' }), "-")
		if part != "" {
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		return "github-repo"
	}
	return strings.Join(out, "-")
}

func dedupeStrings(items []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}
