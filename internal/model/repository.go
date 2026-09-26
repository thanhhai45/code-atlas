// Package model holds the normalized domain types shared by the API and crawler.
package model

import (
	"strings"
	"time"
	"unicode/utf8"
)

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
	// Embedding is the dense vector of EmbeddingText, produced by the ai-worker.
	// It is stored in PostgreSQL so reindexing never needs to re-embed.
	Embedding []float32 `json:"-"`
}

// EmbeddingText is the text a repository's embedding is computed from. The
// model reads at most 256 word pieces, so the fields that describe what a
// project is come first and the README only fills the remaining budget.
func (r Repository) EmbeddingText() string {
	var b strings.Builder
	b.WriteString(r.Name)
	if r.Description != "" {
		b.WriteString(". ")
		b.WriteString(r.Description)
	}
	if len(r.Topics) > 0 {
		b.WriteString(". Topics: ")
		b.WriteString(strings.Join(r.Topics, ", "))
	}
	if r.Language != "" {
		b.WriteString(". Language: ")
		b.WriteString(r.Language)
	}
	if r.Readme != "" {
		readme := r.Readme
		if len(readme) > 1_000 {
			readme = readme[:1_000]
			for len(readme) > 0 && !utf8.ValidString(readme) {
				readme = readme[:len(readme)-1]
			}
		}
		b.WriteString(". ")
		b.WriteString(readme)
	}
	return b.String()
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
