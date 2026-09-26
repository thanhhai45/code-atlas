// Package search talks to Elasticsearch over its REST API.
//
// A thin net/http client is used on purpose instead of an SDK: every request
// body is plain Query DSL, which keeps the Elasticsearch concepts visible.
package search

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/thanhhai45/code-atlas/internal/model"
)

//go:embed index.json
var indexDefinition []byte

// IndexDefinition returns the settings + mappings used for new repository indices.
func IndexDefinition() []byte { return indexDefinition }

var ErrNotFound = errors.New("not found")

type Client struct {
	baseURL string
	alias   string
	http    *http.Client
}

func NewClient(baseURL, alias string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		alias:   alias,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) Alias() string { return c.alias }

// ESError is a non-2xx response from Elasticsearch.
type ESError struct {
	Status int
	Body   string
}

func (e *ESError) Error() string {
	return fmt.Sprintf("elasticsearch: status %d: %s", e.Status, e.Body)
}

func (c *Client) do(ctx context.Context, method, path string, body any, contentType string, out any) error {
	var reader io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		reader = bytes.NewReader(b)
	default:
		buf, err := json.Marshal(b)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	if reader != nil {
		if contentType == "" {
			contentType = "application/json"
		}
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: %s", ErrNotFound, truncate(string(data), 500))
	}
	if resp.StatusCode >= 300 {
		return &ESError{Status: resp.StatusCode, Body: truncate(string(data), 2000)}
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// Ping returns the cluster health status (green / yellow / red).
func (c *Client) Ping(ctx context.Context) (string, error) {
	var health struct {
		Status string `json:"status"`
	}
	err := c.do(ctx, http.MethodGet, "/_cluster/health", nil, "", &health)
	return health.Status, err
}

// AliasTargets returns the concrete indices the alias currently points to.
func (c *Client) AliasTargets(ctx context.Context) ([]string, error) {
	var res map[string]json.RawMessage
	err := c.do(ctx, http.MethodGet, "/_alias/"+url.PathEscape(c.alias), nil, "", &res)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(res))
	for name := range res {
		out = append(out, name)
	}
	return out, nil
}

// NewIndexName returns a versioned concrete index name, e.g. repositories_20260925t162100.
func (c *Client) NewIndexName(now time.Time) string {
	return c.alias + "_" + strings.ToLower(now.UTC().Format("20060102t150405"))
}

// CreateIndex creates a concrete index with the embedded settings + mappings.
func (c *Client) CreateIndex(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodPut, "/"+url.PathEscape(name), indexDefinition, "", nil)
}

func (c *Client) DeleteIndex(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/"+url.PathEscape(name), nil, "", nil)
}

// EnsureIndex makes sure the alias exists, creating a first versioned index if needed.
func (c *Client) EnsureIndex(ctx context.Context) error {
	targets, err := c.AliasTargets(ctx)
	if err != nil || len(targets) > 0 {
		return err
	}
	name := c.NewIndexName(time.Now())
	if err := c.CreateIndex(ctx, name); err != nil {
		return err
	}
	return c.SwapAlias(ctx, name, nil)
}

// SwapAlias atomically points the alias at newIndex and removes it from oldIndices.
func (c *Client) SwapAlias(ctx context.Context, newIndex string, oldIndices []string) error {
	actions := []map[string]any{}
	for _, old := range oldIndices {
		actions = append(actions, map[string]any{"remove": map[string]any{"index": old, "alias": c.alias}})
	}
	actions = append(actions, map[string]any{"add": map[string]any{"index": newIndex, "alias": c.alias, "is_write_index": true}})
	return c.do(ctx, http.MethodPost, "/_aliases", map[string]any{"actions": actions}, "", nil)
}

// Refresh makes recently indexed documents visible to search.
func (c *Client) Refresh(ctx context.Context, index string) error {
	return c.do(ctx, http.MethodPost, "/"+url.PathEscape(index)+"/_refresh", nil, "", nil)
}

// BulkResult summarizes a _bulk call. Item-level failures do not fail the request.
type BulkResult struct {
	Indexed int
	Failed  int
	Errors  []string
}

// BulkIndex indexes repositories into index (or the alias when index is empty).
// The GitHub id is the document _id, so re-indexing the same repository overwrites it.
func (c *Client) BulkIndex(ctx context.Context, index string, repos []model.Repository) (BulkResult, error) {
	if index == "" {
		index = c.alias
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, r := range repos {
		if err := enc.Encode(map[string]any{"index": map[string]any{"_index": index, "_id": strconv.FormatInt(r.ID, 10)}}); err != nil {
			return BulkResult{}, err
		}
		if err := enc.Encode(ToDocument(r)); err != nil {
			return BulkResult{}, err
		}
	}
	var res struct {
		Errors bool `json:"errors"`
		Items  []map[string]struct {
			Status int             `json:"status"`
			ID     string          `json:"_id"`
			Error  json.RawMessage `json:"error"`
		} `json:"items"`
	}
	if err := c.do(ctx, http.MethodPost, "/_bulk", buf.Bytes(), "application/x-ndjson", &res); err != nil {
		return BulkResult{}, err
	}
	out := BulkResult{}
	for _, item := range res.Items {
		for _, v := range item {
			if v.Status >= 300 {
				out.Failed++
				if len(out.Errors) < 10 {
					out.Errors = append(out.Errors, fmt.Sprintf("id=%s status=%d %s", v.ID, v.Status, truncate(string(v.Error), 300)))
				}
			} else {
				out.Indexed++
			}
		}
	}
	return out, nil
}

// ToDocument converts a repository into its Elasticsearch document.
func ToDocument(r model.Repository) map[string]any {
	doc := map[string]any{
		"id":          r.ID,
		"name":        r.Name,
		"full_name":   r.FullName,
		"owner":       r.Owner,
		"description": r.Description,
		"readme":      model.TruncateReadme(r.Readme),
		"url":         r.URL,
		"topics":      nonNil(r.Topics),
		"stars":       r.Stars,
		"forks":       r.Forks,
		"watchers":    r.Watchers,
		"open_issues": r.OpenIssues,
		"archived":    r.Archived,
		"fork":        r.Fork,
		"created_at":  r.CreatedAt,
		"updated_at":  r.UpdatedAt,
		"pushed_at":   r.PushedAt,
	}
	optional := map[string]string{
		"homepage": r.Homepage, "language": r.Language, "license": r.License,
		"license_name": r.LicenseName, "default_branch": r.DefaultBranch,
	}
	for k, v := range optional {
		if v != "" {
			doc[k] = v
		}
	}
	if len(r.Embedding) > 0 {
		doc["embedding"] = r.Embedding
	}
	for k, v := range map[string][]string{"categories": r.Categories, "technologies": r.Technologies, "use_cases": r.UseCases} {
		if len(v) > 0 {
			doc[k] = v
		}
	}
	return doc
}

// RawSearch runs a Query DSL body against the alias and decodes the response.
func (c *Client) RawSearch(ctx context.Context, body map[string]any, out any) error {
	return c.do(ctx, http.MethodPost, "/"+url.PathEscape(c.alias)+"/_search", body, "", out)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
