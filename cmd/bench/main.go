// Command bench measures search latency (P50/P95/P99), throughput and error
// rate for the query types of roadmap Phase 10.
//
//	datagen -n 100000                                   # load synthetic data first
//	bench                                               # all workloads, 8 workers, 20s each
//	bench -workloads full_text,hybrid -c 1,4,16 -d 30s  # concurrency sweep
//	bench -target api -url http://localhost:8080        # end to end through the Go API
//
// Results are only comparable between runs on the same hardware, data and
// settings; the JSON report records those settings.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/thanhhai45/code-atlas/internal/bench"
	"github.com/thanhhai45/code-atlas/internal/config"
	"github.com/thanhhai45/code-atlas/internal/embed"
	"github.com/thanhhai45/code-atlas/internal/search"
	"github.com/thanhhai45/code-atlas/internal/synth"
)

type Report struct {
	Target     string            `json:"target"`
	Alias      string            `json:"alias"`
	Index      search.IndexStats `json:"index"`
	Duration   string            `json:"duration"`
	Warmup     string            `json:"warmup"`
	Seed       uint64            `json:"seed"`
	ClientCPUs int               `json:"client_cpus"`
	StartedAt  time.Time         `json:"started_at"`
	Results    []bench.Summary   `json:"results"`
}

func main() {
	cfg := config.Load()
	target := flag.String("target", "es", "es (Elasticsearch directly) or api (the Go API)")
	baseURL := flag.String("url", "", "base URL (default: ELASTICSEARCH_URL for es, http://localhost:8080 for api)")
	alias := flag.String("alias", "repositories_bench", "index alias to query (es target) and to size the data set")
	workloads := flag.String("workloads", "all", "comma-separated workloads, or all")
	conc := flag.String("c", "8", "comma-separated concurrency levels to sweep")
	duration := flag.Duration("d", 20*time.Second, "measurement time per workload and concurrency")
	warmup := flag.Duration("warmup", 3*time.Second, "warm-up time before each measurement")
	seed := flag.Uint64("seed", 1, "must match the datagen seed, so queries target existing documents")
	maxErr := flag.Float64("max-error-rate", 0.01, "exit 1 if any workload exceeds this error rate")
	jsonOut := flag.String("json", "", "write the report to this file")
	list := flag.Bool("list", false, "list workloads and exit")
	flag.Parse()

	if *baseURL == "" {
		*baseURL = cfg.ElasticsearchURL
		if *target == "api" {
			*baseURL = "http://localhost:8080"
		}
	}
	if err := run(*target, *baseURL, cfg.ElasticsearchURL, *alias, *workloads, *conc, *duration, *warmup, *seed, *maxErr, *jsonOut, *list); err != nil {
		slog.Error("bench failed", "err", err)
		os.Exit(1)
	}
}

func run(target, baseURL, esURL, alias, workloads, concList string, duration, warmup time.Duration, seed uint64, maxErr float64, jsonOut string, list bool) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	es := search.NewClient(esURL, alias)
	stats, err := es.Stats(ctx, alias)
	if err != nil && !list {
		return fmt.Errorf("read index stats for %q (run datagen first): %w", alias, err)
	}
	env := bench.Env{Gen: synth.New(seed, embed.Dim), Docs: int(stats.Docs), Alias: alias}

	var all []bench.Workload
	switch target {
	case "es":
		all = bench.ESWorkloads(env)
	case "api":
		all = bench.APIWorkloads(env)
	default:
		return fmt.Errorf("unknown target %q", target)
	}
	if list {
		for _, w := range all {
			fmt.Printf("%-15s %s\n", w.Name, w.Description)
		}
		return nil
	}
	selected, err := bench.Select(all, workloads)
	if err != nil {
		return err
	}
	if target == "es" {
		hasVectors, err := hasEmbeddings(ctx, es)
		if err != nil {
			return err
		}
		if !hasVectors {
			selected = dropVectorWorkloads(selected)
		}
	}

	var levels []int
	for _, s := range strings.Split(concList, ",") {
		c, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil || c < 1 {
			return fmt.Errorf("bad concurrency %q", s)
		}
		levels = append(levels, c)
	}

	rep := Report{Target: target, Alias: alias, Index: stats, Duration: duration.String(), Warmup: warmup.String(),
		Seed: seed, ClientCPUs: runtime.NumCPU(), StartedAt: time.Now().UTC()}
	fmt.Printf("target %s (%s), alias %s: %d docs, %.1f MB, %d segments\n\n", target, baseURL, alias,
		stats.Docs, float64(stats.StoreBytes)/1e6, stats.Segments)
	const row = "%-15s %4v %9v %7v %9v %8v %8v %8v %8v %10v %10v\n"
	fmt.Printf(row, "workload", "c", "requests", "errors", "qps", "p50 ms", "p95 ms", "p99 ms", "max ms", "ES took ms", "mean hits")
	var failed []string
	for _, c := range levels {
		rn := bench.Runner{BaseURL: baseURL, Concurrency: c, Duration: duration, Warmup: warmup, Seed: seed, HTTP: bench.NewHTTPClient(c)}
		for _, w := range selected {
			if ctx.Err() != nil {
				break
			}
			s := rn.Run(ctx, w)
			rep.Results = append(rep.Results, s)
			f := func(x float64) string { return strconv.FormatFloat(x, 'f', 1, 64) }
			fmt.Printf(row, s.Workload, c, s.Requests, s.Errors, f(s.QPS), f(s.P50ms), f(s.P95ms), f(s.P99ms), f(s.MaxMs),
				f(s.MeanTookMs), strconv.FormatFloat(s.MeanHits, 'f', 0, 64))
			if s.ErrorRate > maxErr {
				failed = append(failed, fmt.Sprintf("%s@c=%d error rate %.3f", s.Workload, c, s.ErrorRate))
			}
			if s.Requests > s.Errors && s.MeanHits == 0 {
				slog.Warn("workload matched no documents; check -seed and the data set", "workload", s.Workload)
			}
		}
	}
	if jsonOut != "" {
		buf, _ := json.MarshalIndent(rep, "", "  ")
		if err := os.WriteFile(jsonOut, append(buf, '\n'), 0o644); err != nil {
			return err
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("error rate above %.3f: %s", maxErr, strings.Join(failed, "; "))
	}
	return ctx.Err()
}

func hasEmbeddings(ctx context.Context, es *search.Client) (bool, error) {
	var res struct {
		Hits struct {
			Total struct {
				Value int64 `json:"value"`
			} `json:"total"`
		} `json:"hits"`
	}
	err := es.RawSearch(ctx, map[string]any{"size": 0, "track_total_hits": 1,
		"query": map[string]any{"exists": map[string]any{"field": "embedding"}}}, &res)
	return res.Hits.Total.Value > 0, err
}

func dropVectorWorkloads(ws []bench.Workload) []bench.Workload {
	out := ws[:0]
	for _, w := range ws {
		if w.NeedsVectors {
			slog.Info("index has no embeddings; skipping", "workload", w.Name)
			continue
		}
		out = append(out, w)
	}
	return out
}
