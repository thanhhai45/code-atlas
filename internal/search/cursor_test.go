package search

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestCursorRoundTrip(t *testing.T) {
	p := Params{Query: "vector database"}
	p.Normalize()
	cur, err := EncodeCursor(sortName(p), []any{json.Number("12.345678"), json.Number("33000"), json.Number("900000012")})
	if err != nil {
		t.Fatal(err)
	}
	values, err := DecodeCursor(cur, p)
	if err != nil {
		t.Fatal(err)
	}
	// Exact values survive: large ids and float scores must not be rounded.
	if got := toJSON(t, values); got != `[12.345678,33000,900000012]` {
		t.Errorf("decoded values = %s", got)
	}
	if strings.ContainsAny(cur, "+/=") {
		t.Errorf("cursor must be URL-safe: %q", cur)
	}
}

func TestDecodeCursorRejectsBadInput(t *testing.T) {
	relevance := Params{Query: "orm"}
	relevance.Normalize()
	stars := Params{Query: "orm", Sort: "stars"}
	stars.Normalize()

	fromRelevance, _ := EncodeCursor(sortName(relevance), []any{json.Number("1.5"), json.Number("10"), json.Number("7")})
	short, _ := EncodeCursor(sortName(stars), []any{json.Number("10")})
	cases := map[string]struct {
		cursor string
		p      Params
	}{
		"not base64":       {"%%%", relevance},
		"not json":         {"bm90IGpzb24", relevance},
		"other sort order": {fromRelevance, stars},
		"wrong length":     {short, stars},
	}
	for name, c := range cases {
		if _, err := DecodeCursor(c.cursor, c.p); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("%s: expected ErrInvalidCursor, got %v", name, err)
		}
	}
}

func TestSortNameTreatsEmptyRelevanceQueryAsStars(t *testing.T) {
	// An empty query sorted by "relevance" is really sorted by stars, so the two
	// share cursors.
	p := Params{}
	p.Normalize()
	if sortName(p) != "stars" {
		t.Errorf("sortName = %q", sortName(p))
	}
}

func TestBuildSearchQueryWithSearchAfter(t *testing.T) {
	p := Params{Query: "orm", Page: 7, Size: 10, SearchAfter: []any{json.Number("3.2"), json.Number("100"), json.Number("5")}}
	body := BuildSearchQuery(p)
	if _, ok := body["from"]; ok {
		t.Error("from must be omitted with search_after")
	}
	if got := toJSON(t, body["search_after"]); got != `[3.2,100,5]` {
		t.Errorf("search_after = %s", got)
	}
	if body["size"] != 10 {
		t.Errorf("size = %v", body["size"])
	}
}

func TestDecodeCursorNormalizesParams(t *testing.T) {
	// Callers may pass Params straight from a request, before Normalize.
	cur, _ := EncodeCursor("relevance:"+DefaultMode, []any{json.Number("1"), json.Number("2"), json.Number("3")})
	if _, err := DecodeCursor(cur, Params{Query: "orm"}); err != nil {
		t.Errorf("un-normalized params: %v", err)
	}
}

func TestCursorSortNameIgnoresVectorPresence(t *testing.T) {
	// The API decodes cursors before embedding the query and encodes them after,
	// so both sides must agree whether or not QueryVector is set yet.
	before := Params{Query: "orm", Mode: ModeHybrid}
	after := before
	after.QueryVector = []float32{0.1, 0.2}
	before.Normalize()
	after.Normalize()
	if sortName(before) != sortName(after) || sortName(before) != "relevance:hybrid" {
		t.Errorf("sortName before=%q after=%q", sortName(before), sortName(after))
	}
	lexical := Params{Query: "orm", Mode: ModeLexical}
	lexical.Normalize()
	if sortName(lexical) == sortName(after) {
		t.Error("lexical and hybrid cursors must not be interchangeable")
	}
}
