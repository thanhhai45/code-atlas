// Package model holds the normalized domain types shared by the API and crawler.
package model

import "time"

// MaxReadmeBytes caps how much README text is stored and indexed. Huge READMEs
// bloat the index and skew BM25 length normalization without improving recall.
const MaxReadmeBytes = 20_000

// Repository is the normalized representation of an open-source repository.
// The GitHub numeric id is used as the primary key in PostgreSQL and as the
// document _id in Elasticsearch, which makes every write idempotent.
type Repository struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	FullName      string    `json:"full_name"`
	Owner         string    `json:"owner"`
	Description   string    `json:"description"`
	URL           string    `json:"url"`
	Homepage      string    `json:"homepage,omitempty"`
	Language      string    `json:"language,omitempty"`
	Topics        []string  `json:"topics"`
	License       string    `json:"license,omitempty"`
	LicenseName   string    `json:"license_name,omitempty"`
	Stars         int       `json:"stars"`
	Forks         int       `json:"forks"`
	Watchers      int       `json:"watchers"`
	OpenIssues    int       `json:"open_issues"`
	DefaultBranch string    `json:"default_branch,omitempty"`
	Archived      bool      `json:"archived"`
	Fork          bool      `json:"fork"`
	Readme        string    `json:"readme,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	PushedAt      time.Time `json:"pushed_at"`
	// Enrichment fields, populated by the AI worker in a later phase.
	Categories   []string `json:"categories,omitempty"`
	Technologies []string `json:"technologies,omitempty"`
	UseCases     []string `json:"use_cases,omitempty"`
}

// TruncateReadme trims the README to MaxReadmeBytes without splitting a UTF-8 rune.
func TruncateReadme(s string) string {
	if len(s) <= MaxReadmeBytes {
		return s
	}
	cut := MaxReadmeBytes
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut]
}
