package search

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrInvalidCursor is returned for a cursor that is malformed or was issued
// for a different sort order.
var ErrInvalidCursor = errors.New("invalid cursor")

// cursor is the decoded form of an opaque pagination cursor. It carries the
// sort values of the last hit on a page, which Elasticsearch's search_after
// uses to continue from there without the from+size depth limit.
type cursor struct {
	Sort   string        `json:"s"`
	Values []json.Number `json:"v"`
}

// EncodeCursor builds the cursor for the page after a hit with the given sort values.
func EncodeCursor(sortName string, values []any) (string, error) {
	nums := make([]json.Number, 0, len(values))
	for _, v := range values {
		switch x := v.(type) {
		case json.Number:
			nums = append(nums, x)
		case float64:
			nums = append(nums, json.Number(fmt.Sprint(x)))
		case int, int64:
			nums = append(nums, json.Number(fmt.Sprint(x)))
		default:
			return "", fmt.Errorf("unsupported sort value %T", v)
		}
	}
	buf, err := json.Marshal(cursor{Sort: sortName, Values: nums})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// DecodeCursor parses a cursor and checks that it matches the sort order of p.
// Every sort clause this package builds is numeric (_score, stars, pushed_at
// as epoch millis, id), so values are kept as exact json.Numbers.
func DecodeCursor(s string, p Params) ([]any, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, ErrInvalidCursor
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var c cursor
	if err := dec.Decode(&c); err != nil {
		return nil, ErrInvalidCursor
	}
	p.Normalize() // p is a copy; compare against the effective sort order
	name, clauses := sortName(p), sortClause(p)
	if c.Sort != name || len(c.Values) != len(clauses) {
		return nil, ErrInvalidCursor
	}
	out := make([]any, len(c.Values))
	for i, v := range c.Values {
		if _, err := v.Float64(); err != nil {
			return nil, ErrInvalidCursor
		}
		out[i] = v
	}
	return out, nil
}

// sortName identifies the effective sort, so a cursor cannot be replayed
// against a different ordering (e.g. relevance vs. stars).
func sortName(p Params) string {
	if p.Sort == "relevance" && p.Query == "" {
		return "stars"
	}
	if p.Sort == "relevance" {
		// Scores differ between retrieval modes, so their cursors do too. Use the
		// requested mode, not EffectiveMode: cursors are decoded before the query
		// is embedded and encoded after.
		mode := ModeLexical
		if p.NeedsVector() {
			mode = p.Mode
		}
		return "relevance:" + mode
	}
	return p.Sort
}
