// Command rankeval measures search relevance against a judgment list with the
// Elasticsearch Ranking Evaluation API (_rank_eval).
//
//	rankeval                                   # evaluate testdata/judgments.json
//	rankeval -v                                # also print the top hits per query
//	rankeval -min-ndcg 0.95 -min-recall 0.8    # exit 1 on a relevance regression
//	rankeval -json docs/benchmarks/rank.json   # save the full result
//
// Every ranking variant is evaluated with the same queries, so the effect of a
// ranking change is visible side by side.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/thanhhai45/code-atlas/internal/config"
	"github.com/thanhhai45/code-atlas/internal/search"
)

// JudgmentFile is the on-disk format of a judgment list.
type JudgmentFile struct {
	Description string     `json:"description"`
	Scale       string     `json:"scale"`
	Queries     []Judgment `json:"queries"`
}

type Judgment struct {
	ID      string         `json:"id"`
	Intent  string         `json:"intent,omitempty"`
	Params  JudgmentParams `json:"params"`
	Ratings map[string]int `json:"ratings"` // full_name -> rating (0..3)
}

type JudgmentParams struct {
	Q            string   `json:"q"`
	Language     []string `json:"language,omitempty"`
	License      []string `json:"license,omitempty"`
	Topic        []string `json:"topic,omitempty"`
	MinStars     *int     `json:"min_stars,omitempty"`
	MaxStars     *int     `json:"max_stars,omitempty"`
	PushedWithin string   `json:"pushed_within,omitempty"`
}

func (p JudgmentParams) toSearch() search.Params {
	return search.Params{
		Query: p.Q, Languages: p.Language, Licenses: p.License, Topics: p.Topic,
		MinStars: p.MinStars, MaxStars: p.MaxStars, PushedWithin: p.PushedWithin,
	}
}

type variant struct {
	Name string
	Opts search.RankingOptions
}

var variants = []variant{
	{Name: "default", Opts: search.RankingOptions{}},
	{Name: "bm25_only", Opts: search.RankingOptions{DisableBusinessSignals: true}},
}

// gridVariants spans the business-signal weights explored by -grid.
func gridVariants() []variant {
	var out []variant
	add := func(s search.Signals) {
		name := fmt.Sprintf("mul base=%g stars=%g rec=%g", s.Base, s.StarsWeight, s.RecencyWeight)
		if s.BoostMode == "sum" {
			name = fmt.Sprintf("sum stars=%g rec=%g", s.StarsWeight, s.RecencyWeight)
		}
		out = append(out, variant{Name: name, Opts: search.RankingOptions{Signals: &s}})
	}
	for _, base := range []float64{0, 1, 2, 5, 10, 20} {
		for _, rec := range []float64{0, 0.5, 1} {
			add(search.Signals{BoostMode: "multiply", Base: base, StarsWeight: 1, RecencyWeight: rec})
		}
	}
	for _, stars := range []float64{0.5, 1, 2, 5} {
		for _, rec := range []float64{0, 1, 2} {
			add(search.Signals{BoostMode: "sum", StarsWeight: stars, RecencyWeight: rec})
		}
	}
	return out
}

// bestVariant picks the highest mean NDCG, breaking ties by MRR, then
// precision, then list order (the grid lists weaker signals first).
func bestVariant(r Report, k int, names []string) string {
	ndcg, mrr := fmt.Sprintf("ndcg@%d", k), fmt.Sprintf("mrr@%d", k)
	best := ""
	for _, n := range names {
		if best == "" {
			best = n
			continue
		}
		a, b := r.Metrics[n], r.Metrics[best]
		switch {
		case a[ndcg] > b[ndcg]+1e-9:
			best = n
		case a[ndcg] < b[ndcg]-1e-9:
		case a[mrr] > b[mrr]+1e-9:
			best = n
		case a[mrr] < b[mrr]-1e-9:
		case a["precision@5"] > b["precision@5"]+1e-9:
			best = n
		}
	}
	return best
}

// Report is the machine-readable result (-json).
type Report struct {
	Index    string                                   `json:"index"`
	Queries  int                                      `json:"queries"`
	Metrics  map[string]map[string]float64            `json:"metrics"`        // variant -> metric -> score
	PerQuery map[string]map[string]map[string]float64 `json:"per_query"`      // variant -> query -> metric -> score
	Unrated  map[string][]string                      `json:"unrated"`        // query -> unrated full names in top k (default variant)
	Best     string                                   `json:"best,omitempty"` // best grid variant (-grid only)
}

func main() {
	path := flag.String("judgments", "testdata/judgments.json", "judgment list")
	k := flag.Int("k", 10, "cut-off rank for NDCG, MRR and recall")
	minNDCG := flag.Float64("min-ndcg", 0, "fail if the default variant's mean NDCG@k is below this value")
	minRecall := flag.Float64("min-recall", 0, "fail if the default variant's mean recall@k is below this value")
	verbose := flag.Bool("v", false, "print the top hits of every query")
	jsonOut := flag.String("json", "", "write the full report to this file")
	grid := flag.Bool("grid", false, "also evaluate a grid of business-signal weights and report the best")
	flag.Parse()
	if *grid {
		variants = append(variants, gridVariants()...)
	}

	if err := run(*path, *k, thresholds{ndcg: *minNDCG, recall: *minRecall}, *verbose, *jsonOut); err != nil {
		slog.Error("rankeval failed", "err", err)
		os.Exit(1)
	}
}

type thresholds struct{ ndcg, recall float64 }

func run(path string, k int, min thresholds, verbose bool, jsonOut string) error {
	ctx := context.Background()
	cfg := config.Load()
	es := search.NewClient(cfg.ElasticsearchURL, cfg.SearchAlias)

	jf, err := loadJudgments(path)
	if err != nil {
		return err
	}
	targets, err := es.AliasTargets(ctx)
	if err != nil {
		return err
	}
	if len(targets) != 1 {
		return fmt.Errorf("alias %q must point to exactly one index, found %v", es.Alias(), targets)
	}
	index := targets[0]
	// Make documents indexed moments ago (e.g. by a seed run in CI) visible.
	if err := es.Refresh(ctx, index); err != nil {
		return err
	}

	// Judgments are written with human-readable full names; resolve them to ids.
	names := map[string]bool{}
	for _, j := range jf.Queries {
		for n := range j.Ratings {
			names[n] = true
		}
	}
	nameList := make([]string, 0, len(names))
	for n := range names {
		nameList = append(nameList, n)
	}
	ids, err := es.ResolveIDs(ctx, nameList)
	if err != nil {
		return err
	}
	var missing []string
	for _, n := range nameList {
		if _, ok := ids[strings.ToLower(n)]; !ok {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("judged repositories are not in the index (stale judgments or missing data): %s", strings.Join(missing, ", "))
	}
	nameByID := map[string]string{}
	for n, id := range ids {
		nameByID[id] = n
	}

	metrics := []map[string]any{search.NDCG(k), search.MRR(k), search.Precision(5), search.Recall(k)}
	report := Report{
		Index: index, Queries: len(jf.Queries),
		Metrics:  map[string]map[string]float64{},
		PerQuery: map[string]map[string]map[string]float64{},
		Unrated:  map[string][]string{},
	}
	var topHits map[string]search.EvalQueryResult

	for _, v := range variants {
		requests := make([]search.EvalRequest, 0, len(jf.Queries))
		for _, j := range jf.Queries {
			req := search.EvalRequest{ID: j.ID, Request: search.BuildRankEvalRequest(j.Params.toSearch(), v.Opts)}
			for n, r := range j.Ratings {
				req.Ratings = append(req.Ratings, search.RatedDoc{ID: ids[strings.ToLower(n)], Rating: r})
			}
			requests = append(requests, req)
		}
		report.Metrics[v.Name] = map[string]float64{}
		report.PerQuery[v.Name] = map[string]map[string]float64{}
		for _, m := range metrics {
			name := search.MetricName(m)
			res, err := es.RankEval(ctx, index, requests, m)
			if err != nil {
				return fmt.Errorf("%s %s: %w", v.Name, name, err)
			}
			if len(res.Failures) > 0 {
				return fmt.Errorf("%s %s: query failures: %v", v.Name, name, res.Failures)
			}
			report.Metrics[v.Name][name] = res.Score
			for id, q := range res.Queries {
				if report.PerQuery[v.Name][id] == nil {
					report.PerQuery[v.Name][id] = map[string]float64{}
				}
				report.PerQuery[v.Name][id][name] = q.Score
			}
			if v.Name == variants[0].Name && name == search.MetricName(search.NDCG(k)) {
				topHits = res.Queries
			}
		}
	}

	if len(variants) > 2 {
		names := make([]string, 0, len(variants)-2)
		for _, v := range variants[2:] {
			names = append(names, v.Name)
		}
		report.Best = bestVariant(report, k, names)
	}

	// Name the unrated documents so the judgment list can be extended.
	var unknown []string
	for _, q := range topHits {
		for _, h := range q.Hits {
			if _, ok := nameByID[h.ID]; !ok {
				unknown = append(unknown, h.ID)
			}
		}
	}
	if len(unknown) > 0 {
		names, err := es.FullNames(ctx, unknown)
		if err != nil {
			return err
		}
		for id, n := range names {
			nameByID[id] = n
		}
	}
	for id, q := range topHits {
		for _, u := range q.UnratedDocs {
			report.Unrated[id] = append(report.Unrated[id], nameByID[u])
		}
	}

	if jsonOut != "" {
		buf, _ := json.MarshalIndent(report, "", "  ")
		if err := os.WriteFile(jsonOut, append(buf, '\n'), 0o644); err != nil {
			return err
		}
	}
	printReport(os.Stdout, jf, report, k)
	if verbose {
		printHits(os.Stdout, jf, topHits, nameByID)
	}
	return checkThresholds(report.Metrics[variants[0].Name], k, min)
}

// checkThresholds gates on NDCG and recall together: Elasticsearch normalizes
// DCG against the ideal ordering of only as many documents as were returned,
// so NDCG alone does not penalize relevant documents that were never retrieved.
func checkThresholds(scores map[string]float64, k int, min thresholds) error {
	var failed []string
	if v := scores[search.MetricName(search.NDCG(k))]; v < min.ndcg {
		failed = append(failed, fmt.Sprintf("NDCG@%d %.4f < %.4f", k, v, min.ndcg))
	}
	if v := scores[search.MetricName(search.Recall(k))]; v < min.recall {
		failed = append(failed, fmt.Sprintf("recall@%d %.4f < %.4f", k, v, min.recall))
	}
	if len(failed) > 0 {
		return fmt.Errorf("relevance regression: %s", strings.Join(failed, "; "))
	}
	return nil
}

func loadJudgments(path string) (JudgmentFile, error) {
	var jf JudgmentFile
	data, err := os.ReadFile(path)
	if err != nil {
		return jf, err
	}
	if err := json.Unmarshal(data, &jf); err != nil {
		return jf, fmt.Errorf("parse %s: %w", path, err)
	}
	return jf, validate(jf)
}

func validate(jf JudgmentFile) error {
	if len(jf.Queries) == 0 {
		return errors.New("judgment list has no queries")
	}
	seen := map[string]bool{}
	for _, j := range jf.Queries {
		switch {
		case j.ID == "":
			return errors.New("every query needs an id")
		case seen[j.ID]:
			return fmt.Errorf("duplicate query id %q", j.ID)
		case strings.TrimSpace(j.Params.Q) == "":
			return fmt.Errorf("query %q has no text", j.ID)
		}
		seen[j.ID] = true
		relevant := false
		for n, r := range j.Ratings {
			if r < 0 || r > 3 {
				return fmt.Errorf("query %q: rating %d for %s is outside 0..3", j.ID, r, n)
			}
			relevant = relevant || r >= 2
		}
		if !relevant {
			return fmt.Errorf("query %q has no document rated >= 2", j.ID)
		}
	}
	return nil
}

func printReport(w *os.File, jf JudgmentFile, r Report, k int) {
	metricNames := []string{fmt.Sprintf("ndcg@%d", k), fmt.Sprintf("mrr@%d", k), "precision@5", fmt.Sprintf("recall@%d", k)}
	ndcg := metricNames[0]

	fmt.Fprintf(w, "index %s, %d queries\n\n", r.Index, r.Queries)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprint(tw, "variant")
	for _, m := range metricNames {
		fmt.Fprintf(tw, "\t%s", m)
	}
	fmt.Fprintln(tw)
	for _, v := range variants {
		fmt.Fprint(tw, v.Name)
		for _, m := range metricNames {
			fmt.Fprintf(tw, "\t%.4f", r.Metrics[v.Name][m])
		}
		fmt.Fprintln(tw)
	}
	tw.Flush()

	// Per-query columns: default, bm25_only and, with -grid, the best grid variant.
	columns := []string{variants[0].Name, variants[1].Name}
	if r.Best != "" {
		fmt.Fprintf(w, "\nbest grid variant: %s\n", r.Best)
		columns = append(columns, r.Best)
	}

	fmt.Fprintf(w, "\nper query (%s)\n", ndcg)
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprint(tw, "query")
	for _, c := range columns {
		fmt.Fprintf(tw, "\t%s", c)
	}
	fmt.Fprintln(tw, "\tunrated in top k")
	for _, j := range jf.Queries {
		fmt.Fprintf(tw, "%s", j.ID)
		for _, c := range columns {
			fmt.Fprintf(tw, "\t%.3f", r.PerQuery[c][j.ID][ndcg])
		}
		fmt.Fprintf(tw, "\t%d\n", len(r.Unrated[j.ID]))
	}
	tw.Flush()
}

func printHits(w *os.File, jf JudgmentFile, hits map[string]search.EvalQueryResult, nameByID map[string]string) {
	fmt.Fprintf(w, "\ntop hits (%s variant)\n", variants[0].Name)
	for _, j := range jf.Queries {
		fmt.Fprintf(w, "\n%s  q=%q\n", j.ID, j.Params.Q)
		for i, h := range hits[j.ID].Hits {
			rating := "?"
			if h.Rating != nil {
				rating = fmt.Sprint(*h.Rating)
			}
			name := nameByID[h.ID]
			if name == "" {
				name = "id:" + h.ID
			}
			fmt.Fprintf(w, "  %2d. [%s] %s\n", i+1, rating, name)
		}
	}
}
