// Package github is a small GitHub REST client built for bulk crawling:
// it paginates, respects primary and secondary rate limits, and retries
// transient failures with exponential backoff and jitter.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/thanhhai45/code-atlas/internal/model"
)

// The Search API returns at most 1000 results per query (10 pages x 100).
const (
	PerPage       = 100
	MaxSearchPage = 10
)

var ErrNotFound = errors.New("github: not found")

type Client struct {
	BaseURL    string
	Token      string
	HTTP       *http.Client
	MaxRetries int
	// MaxWait bounds a single rate-limit sleep so a misconfigured reset header
	// cannot park the crawler for an hour.
	MaxWait time.Duration
	// SearchInterval spaces Search API calls (30 req/min authenticated, 10 unauthenticated).
	SearchInterval time.Duration
	Logger         *slog.Logger
	// sleep is swappable in tests.
	sleep      func(ctx context.Context, d time.Duration) error
	lastSearch time.Time
}

func NewClient(baseURL, token string) *Client {
	interval := 6500 * time.Millisecond
	if token != "" {
		interval = 2100 * time.Millisecond
	}
	return &Client{
		BaseURL:        strings.TrimRight(baseURL, "/"),
		Token:          token,
		HTTP:           &http.Client{Timeout: 30 * time.Second},
		MaxRetries:     5,
		MaxWait:        15 * time.Minute,
		SearchInterval: interval,
		Logger:         slog.Default(),
		sleep:          sleepCtx,
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// retryDelay decides whether and how long to wait before retrying a response.
// It returns ok=false for responses that must not be retried.
func (c *Client) retryDelay(resp *http.Response, attempt int) (time.Duration, bool) {
	backoff := time.Duration(1<<attempt) * time.Second
	backoff += time.Duration(rand.Int64N(int64(500 * time.Millisecond)))

	switch {
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		// Secondary rate limit: GitHub sends Retry-After.
		if ra, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil {
			return time.Duration(ra) * time.Second, true
		}
		// Primary rate limit: wait until the window resets.
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			if reset, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
				return time.Until(time.Unix(reset, 0)) + time.Second, true
			}
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			return backoff, true
		}
		return 0, false // a plain 403 (e.g. forbidden resource) is permanent
	case resp.StatusCode >= 500:
		return backoff, true
	}
	return 0, false
}

func (c *Client) get(ctx context.Context, path string, accept string) ([]byte, http.Header, error) {
	var lastErr error
	for attempt := 0; attempt <= c.MaxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
		if err != nil {
			return nil, nil, err
		}
		if accept == "" {
			accept = "application/vnd.github+json"
		}
		req.Header.Set("Accept", accept)
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		req.Header.Set("User-Agent", "code-atlas-crawler")
		if c.Token != "" {
			req.Header.Set("Authorization", "Bearer "+c.Token)
		}

		resp, err := c.HTTP.Do(req)
		if err != nil {
			// Network errors are transient.
			lastErr = err
			if err := c.sleep(ctx, time.Duration(1<<attempt)*time.Second); err != nil {
				return nil, nil, err
			}
			continue
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if resp.StatusCode == http.StatusOK {
			return body, resp.Header, nil
		}
		if resp.StatusCode == http.StatusNotFound {
			return nil, resp.Header, ErrNotFound
		}
		lastErr = fmt.Errorf("github: GET %s: status %d: %s", path, resp.StatusCode, truncate(string(body), 300))
		wait, retry := c.retryDelay(resp, attempt)
		if !retry {
			return nil, resp.Header, lastErr
		}
		if wait > c.MaxWait {
			wait = c.MaxWait
		}
		c.Logger.Warn("github request throttled or failed, retrying",
			"path", path, "status", resp.StatusCode, "attempt", attempt+1, "wait", wait.Round(time.Second))
		if err := c.sleep(ctx, wait); err != nil {
			return nil, nil, err
		}
	}
	return nil, nil, fmt.Errorf("github: giving up after %d retries: %w", c.MaxRetries, lastErr)
}

// apiRepo mirrors the fields we use from GitHub's repository object.
type apiRepo struct {
	ID          int64                  `json:"id"`
	Name        string                 `json:"name"`
	FullName    string                 `json:"full_name"`
	Owner       struct{ Login string } `json:"owner"`
	Description *string                `json:"description"`
	HTMLURL     string                 `json:"html_url"`
	Homepage    *string                `json:"homepage"`
	Language    *string                `json:"language"`
	Topics      []string               `json:"topics"`
	License     *struct {
		SPDXID string `json:"spdx_id"`
		Name   string `json:"name"`
	} `json:"license"`
	Stars int `json:"stargazers_count"`
	Forks int `json:"forks_count"`
	// subscribers_count is only present on the single-repository endpoint, not in search results.
	Watchers      int       `json:"subscribers_count"`
	OpenIssues    int       `json:"open_issues_count"`
	DefaultBranch string    `json:"default_branch"`
	Archived      bool      `json:"archived"`
	Fork          bool      `json:"fork"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	PushedAt      time.Time `json:"pushed_at"`
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}

// normalize maps the GitHub payload onto the domain model.
func (r apiRepo) normalize() model.Repository {
	repo := model.Repository{
		ID:            r.ID,
		Name:          r.Name,
		FullName:      r.FullName,
		Owner:         r.Owner.Login,
		Description:   str(r.Description),
		URL:           r.HTMLURL,
		Homepage:      str(r.Homepage),
		Language:      str(r.Language),
		Topics:        r.Topics,
		Stars:         r.Stars,
		Forks:         r.Forks,
		Watchers:      r.Watchers,
		OpenIssues:    r.OpenIssues,
		DefaultBranch: r.DefaultBranch,
		Archived:      r.Archived,
		Fork:          r.Fork,
		CreatedAt:     r.CreatedAt,
		UpdatedAt:     r.UpdatedAt,
		PushedAt:      r.PushedAt,
	}
	if repo.Topics == nil {
		repo.Topics = []string{}
	}
	// "NOASSERTION" means GitHub could not identify the license; treat as unknown.
	if r.License != nil && r.License.SPDXID != "" && r.License.SPDXID != "NOASSERTION" {
		repo.License = r.License.SPDXID
		repo.LicenseName = r.License.Name
	}
	return repo
}

type searchResponse struct {
	TotalCount        int       `json:"total_count"`
	IncompleteResults bool      `json:"incomplete_results"`
	Items             []apiRepo `json:"items"`
}

// SearchPage fetches one page (1-based) of repository search results sorted by stars.
func (c *Client) SearchPage(ctx context.Context, query string, page int) ([]model.Repository, int, error) {
	if wait := c.SearchInterval - time.Since(c.lastSearch); wait > 0 {
		if err := c.sleep(ctx, wait); err != nil {
			return nil, 0, err
		}
	}
	c.lastSearch = time.Now()

	v := url.Values{}
	v.Set("q", query)
	v.Set("sort", "stars")
	v.Set("order", "desc")
	v.Set("per_page", strconv.Itoa(PerPage))
	v.Set("page", strconv.Itoa(page))
	body, _, err := c.get(ctx, "/search/repositories?"+v.Encode(), "")
	if err != nil {
		return nil, 0, err
	}
	var res searchResponse
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, 0, err
	}
	out := make([]model.Repository, 0, len(res.Items))
	for _, item := range res.Items {
		out = append(out, item.normalize())
	}
	return out, res.TotalCount, nil
}

// SearchOptions controls a multi-query crawl.
type SearchOptions struct {
	// Qualifiers are extra search qualifiers, e.g. "language:go topic:database".
	// Do not include a stars: qualifier; star ranges are managed by the crawler.
	Qualifiers string
	MinStars   int
	Max        int
}

// buildQuery composes the qualifiers with a star range. maxStars < 0 means unbounded.
func buildQuery(qualifiers string, minStars, maxStars int) string {
	upper := "*"
	if maxStars >= 0 {
		upper = strconv.Itoa(maxStars)
	}
	return strings.TrimSpace(qualifiers + " stars:" + strconv.Itoa(minStars) + ".." + upper)
}

// SearchAll works around the 1000-results-per-query cap with a "star cursor":
// results are sorted by stars desc, and once a query is exhausted the next one
// is restricted to stars <= the lowest star count seen. Duplicates at the
// boundary are dropped by id. fn receives each page as it arrives.
func (c *Client) SearchAll(ctx context.Context, opts SearchOptions, fn func([]model.Repository) error) (int, error) {
	seen := map[int64]struct{}{}
	upper := -1
	for {
		query := buildQuery(opts.Qualifiers, opts.MinStars, upper)
		lowest := -1
		for page := 1; page <= MaxSearchPage; page++ {
			repos, total, err := c.SearchPage(ctx, query, page)
			if err != nil {
				return len(seen), err
			}
			if page == 1 {
				c.Logger.Info("github search", "query", query, "total", total)
			}
			fresh := make([]model.Repository, 0, len(repos))
			for _, r := range repos {
				if lowest < 0 || r.Stars < lowest {
					lowest = r.Stars
				}
				if _, dup := seen[r.ID]; dup {
					continue
				}
				if opts.Max > 0 && len(seen) >= opts.Max {
					break
				}
				seen[r.ID] = struct{}{}
				fresh = append(fresh, r)
			}
			if len(fresh) > 0 {
				if err := fn(fresh); err != nil {
					return len(seen), err
				}
			}
			if opts.Max > 0 && len(seen) >= opts.Max {
				return len(seen), nil
			}
			if len(repos) < PerPage {
				return len(seen), nil // this query is exhausted and was under the cap
			}
		}
		// The query hit the 1000 cap. Move the cursor down.
		if lowest < 0 || (upper >= 0 && lowest >= upper) {
			// Every result in the window had the same star count: the cursor cannot
			// advance. Finer slicing (e.g. by created date) is a future improvement.
			c.Logger.Warn("star cursor cannot advance; stopping", "stars", lowest)
			return len(seen), nil
		}
		upper = lowest
	}
}

// Readme fetches the raw README for owner/name. A missing README returns "".
func (c *Client) Readme(ctx context.Context, fullName string) (string, error) {
	body, _, err := c.get(ctx, "/repos/"+fullName+"/readme", "application/vnd.github.raw+json")
	if errors.Is(err, ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return model.TruncateReadme(string(body)), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
