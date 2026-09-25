package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/thanhhai45/code-atlas/internal/model"
)

// The bundled judgment list must stay valid and in sync with the seed data,
// otherwise the CI relevance gate fails for reasons unrelated to ranking.
func TestBundledJudgmentsMatchSeedData(t *testing.T) {
	jf, err := loadJudgments("../../testdata/judgments.json")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../../testdata/seed_repositories.json")
	if err != nil {
		t.Fatal(err)
	}
	var repos []model.Repository
	if err := json.Unmarshal(data, &repos); err != nil {
		t.Fatal(err)
	}
	seed := map[string]model.Repository{}
	for _, r := range repos {
		seed[strings.ToLower(r.FullName)] = r
	}
	for _, j := range jf.Queries {
		for name := range j.Ratings {
			r, ok := seed[strings.ToLower(name)]
			if !ok {
				t.Errorf("query %s rates %s, which is not in the seed data", j.ID, name)
				continue
			}
			if r.Archived {
				t.Errorf("query %s rates archived %s, which search excludes by default", j.ID, name)
			}
		}
	}
}

func TestValidateRejectsBadJudgments(t *testing.T) {
	cases := map[string]JudgmentFile{
		"empty":         {},
		"missing id":    {Queries: []Judgment{{Params: JudgmentParams{Q: "x"}, Ratings: map[string]int{"a/b": 3}}}},
		"no text":       {Queries: []Judgment{{ID: "a", Ratings: map[string]int{"a/b": 3}}}},
		"bad rating":    {Queries: []Judgment{{ID: "a", Params: JudgmentParams{Q: "x"}, Ratings: map[string]int{"a/b": 4}}}},
		"none relevant": {Queries: []Judgment{{ID: "a", Params: JudgmentParams{Q: "x"}, Ratings: map[string]int{"a/b": 1}}}},
		"duplicate id": {Queries: []Judgment{
			{ID: "a", Params: JudgmentParams{Q: "x"}, Ratings: map[string]int{"a/b": 3}},
			{ID: "a", Params: JudgmentParams{Q: "y"}, Ratings: map[string]int{"a/b": 3}},
		}},
	}
	for name, jf := range cases {
		if err := validate(jf); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
}

func TestCheckThresholds(t *testing.T) {
	scores := map[string]float64{"ndcg@10": 0.98, "recall@10": 0.70}
	if err := checkThresholds(scores, 10, thresholds{ndcg: 0.95}); err != nil {
		t.Errorf("unexpected failure: %v", err)
	}
	err := checkThresholds(scores, 10, thresholds{ndcg: 0.95, recall: 0.8})
	if err == nil || !strings.Contains(err.Error(), "recall@10") {
		t.Errorf("recall regression must fail even when NDCG passes, got %v", err)
	}
}

func TestBestVariant(t *testing.T) {
	r := Report{Metrics: map[string]map[string]float64{
		"a": {"ndcg@10": 0.90, "mrr@10": 1.0},
		"b": {"ndcg@10": 0.95, "mrr@10": 0.9},
		"c": {"ndcg@10": 0.95, "mrr@10": 1.0},
		"d": {"ndcg@10": 0.95, "mrr@10": 1.0}, // tie with c: the earlier one wins
	}}
	if got := bestVariant(r, 10, []string{"a", "b", "c", "d"}); got != "c" {
		t.Errorf("bestVariant = %q, want c", got)
	}
}

func TestGridVariantNamesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, v := range gridVariants() {
		if seen[v.Name] {
			t.Errorf("duplicate grid variant %q", v.Name)
		}
		seen[v.Name] = true
	}
}
