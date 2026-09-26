// Package synth generates realistic synthetic repositories for benchmarks.
//
// Real GitHub data is the goal, but benchmarking needs data sets far larger
// than a crawl can produce quickly (and reproducibly). The generator mimics the
// shapes that matter for search performance:
//
//   - stars follow a Pareto (power-law) distribution, like GitHub's;
//   - topics and description words are drawn from Zipf distributions, so term
//     frequencies (and therefore BM25 posting-list lengths) are skewed;
//   - languages and licenses follow weighted distributions;
//   - embeddings cluster around a centroid per main topic, so kNN search runs
//     on structured data rather than uniform noise.
//
// Repository i is generated from its own seeded RNG, so any document can be
// regenerated independently (and generation parallelizes trivially).
package synth

import (
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/thanhhai45/code-atlas/internal/model"
)

// IDBase keeps synthetic ids clear of the seed data (900000001+).
const IDBase = 1_000_000_000

type weighted struct {
	value  string
	weight float64
}

var languages = []weighted{
	{"JavaScript", 17}, {"Python", 16}, {"TypeScript", 11}, {"Go", 8}, {"Java", 8}, {"Rust", 5},
	{"C++", 5}, {"C", 4}, {"PHP", 4}, {"C#", 4}, {"Ruby", 3}, {"Shell", 3}, {"Kotlin", 2},
	{"Swift", 2}, {"Scala", 1}, {"Dart", 1}, {"Elixir", 1}, {"Haskell", 0.5}, {"Lua", 0.5}, {"", 4},
}

var licenses = []weighted{
	{"MIT", 45}, {"Apache-2.0", 22}, {"", 13}, {"GPL-3.0", 7}, {"BSD-3-Clause", 4}, {"AGPL-3.0", 2},
	{"MPL-2.0", 2}, {"LGPL-3.0", 2}, {"BSD-2-Clause", 1.5}, {"Unlicense", 1}, {"ISC", 0.5},
}

// Topics, most common first (Zipf ranks).
var Topics = strings.Fields(`javascript python react api cli library framework database web machine-learning
docker kubernetes golang typescript rust nodejs http json testing devops linux security
llm ai deep-learning search-engine monitoring logging orm graphql rest-api microservices
authentication cache redis postgresql mysql mongodb elasticsearch kafka messaging event-driven
serverless aws terraform ansible ci-cd github-actions nextjs vue angular svelte tailwindcss
css html frontend backend fullstack mobile android ios flutter game-engine graphics
compiler parser interpreter wasm blockchain cryptography networking grpc websocket
data-science pandas visualization dashboard analytics etl streaming spark hadoop
vector-database embeddings rag nlp computer-vision pytorch tensorflow transformers
scraping crawler automation bot discord telegram chatbot markdown static-site-generator
cms ecommerce payments oauth jwt encryption vpn proxy load-balancer observability tracing
prometheus grafana opentelemetry benchmark performance concurrency async functional-programming
embedded iot arduino raspberry-pi robotics simulation physics audio video image-processing
pdf excel email calendar notes editor terminal shell dotfiles vim neovim emacs vscode
plugin theme icons fonts i18n accessibility documentation tutorial awesome-list education`)

var adjectives = strings.Fields(`fast lightweight simple modern minimal scalable distributed high-performance
secure type-safe declarative reactive embeddable extensible zero-dependency cloud-native
open-source real-time async blazing-fast tiny production-ready self-hosted pluggable portable
concurrent efficient robust flexible powerful friendly composable idiomatic`)

var nouns = strings.Fields(`library framework toolkit engine client server SDK CLI tool platform database
parser compiler runtime proxy gateway scheduler queue cache store index crawler generator
validator router middleware plugin extension wrapper adapter driver bot dashboard editor`)

var verbs = strings.Fields(`build manage deploy monitor test parse query index search stream analyze
visualize automate secure scale cache validate generate render schedule orchestrate`)

var objects = strings.Fields(`APIs containers clusters logs metrics documents events workflows pipelines
queries images videos datasets models embeddings websites services microservices infrastructure`)

var syllables = strings.Fields(`ka ro mi te zu la no vi sa po de ri ku ma to ne fi ga lo xe be qu ha ji yo`)

// Generator produces repositories deterministically from a seed.
type Generator struct {
	Seed uint64
	// Now anchors creation and push dates; set it for reproducible data.
	Now       time.Time
	Readme    bool // generate a short README body
	Vectors   bool // generate an embedding
	Dim       int
	centroids [][]float32
	langCDF   []float64
	licCDF    []float64
}

func New(seed uint64, dim int) *Generator {
	g := &Generator{Seed: seed, Now: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Readme: true, Vectors: true, Dim: dim}
	g.langCDF = cdf(languages)
	g.licCDF = cdf(licenses)
	r := rand.New(rand.NewPCG(seed, 0xC0FFEE))
	g.centroids = make([][]float32, len(Topics))
	for i := range g.centroids {
		g.centroids[i] = randomUnit(r, dim)
	}
	return g
}

func cdf(ws []weighted) []float64 {
	out := make([]float64, len(ws))
	total := 0.0
	for _, w := range ws {
		total += w.weight
	}
	acc := 0.0
	for i, w := range ws {
		acc += w.weight / total
		out[i] = acc
	}
	return out
}

func pick(r *rand.Rand, ws []weighted, c []float64) string {
	u := r.Float64()
	for i, p := range c {
		if u <= p {
			return ws[i].value
		}
	}
	return ws[len(ws)-1].value
}

// zipfIndex draws an index in [0, n) with P(k) ∝ 1/(k+1)^s.
func zipfIndex(r *rand.Rand, n int, s float64) int {
	z := rand.NewZipf(r, s, 1, uint64(n-1))
	return int(z.Uint64())
}

func randomUnit(r *rand.Rand, dim int) []float32 {
	v := make([]float32, dim)
	var norm float64
	for i := range v {
		x := r.NormFloat64()
		v[i] = float32(x)
		norm += x * x
	}
	norm = math.Sqrt(norm)
	for i := range v {
		v[i] /= float32(norm)
	}
	return v
}

func (g *Generator) rng(i int) *rand.Rand { return rand.New(rand.NewPCG(g.Seed, uint64(i)+1)) }

// Name returns the full name of repository i without generating the rest.
func (g *Generator) Name(i int) string { return g.Repo(i).FullName }

// Repo generates repository i.
func (g *Generator) Repo(i int) model.Repository {
	r := g.rng(i)
	owner := word(r, 2+r.IntN(2)) + fmt.Sprint(r.IntN(100))
	mainTopic := zipfIndex(r, len(Topics), 1.1)
	topics := []string{Topics[mainTopic]}
	for range r.IntN(5) {
		t := Topics[zipfIndex(r, len(Topics), 1.1)]
		if !contains(topics, t) {
			topics = append(topics, t)
		}
	}
	noun := nouns[r.IntN(len(nouns))]
	name := strings.ToLower(Topics[mainTopic] + "-" + noun)
	if r.IntN(3) == 0 {
		name = strings.ToLower(adjectives[r.IntN(len(adjectives))] + "-" + name)
	}
	lang := pick(r, languages, g.langCDF)
	desc := g.sentence(r, topics, lang, noun)

	// Pareto(xm=3, alpha=1.05): most repositories have a handful of stars, a few have 100K+.
	stars := int(3/math.Pow(1-r.Float64(), 1/1.05)) - 3
	stars = min(stars, 450_000)

	created := g.Now.Add(-time.Duration(r.Int64N(int64(17 * 365 * 24 * time.Hour))))
	sinceCreated := g.Now.Sub(created)
	// Popular repositories are more likely to be active recently.
	activity := math.Pow(r.Float64(), 1+math.Log10(float64(stars+10)))
	pushed := g.Now.Add(-time.Duration(activity * float64(sinceCreated)))

	repo := model.Repository{
		ID:            int64(IDBase + i),
		Name:          name,
		FullName:      owner + "/" + name,
		Owner:         owner,
		Description:   desc,
		URL:           "https://github.com/" + owner + "/" + name,
		Language:      lang,
		Topics:        topics,
		License:       pick(r, licenses, g.licCDF),
		Stars:         stars,
		Forks:         int(float64(stars) * (0.05 + 0.2*r.Float64())),
		OpenIssues:    r.IntN(1 + stars/20),
		DefaultBranch: "main",
		Archived:      r.Float64() < 0.03,
		Fork:          r.Float64() < 0.05,
		CreatedAt:     created,
		UpdatedAt:     pushed,
		PushedAt:      pushed,
	}
	if g.Readme {
		var b strings.Builder
		for range 3 + r.IntN(5) {
			b.WriteString(g.sentence(r, topics, lang, nouns[r.IntN(len(nouns))]))
			b.WriteString(" ")
		}
		repo.Readme = b.String()
	}
	if g.Vectors {
		repo.Embedding = g.vector(r, mainTopic)
	}
	return repo
}

// QueryVector returns a vector near the centroid of topic t, for kNN queries.
func (g *Generator) QueryVector(r *rand.Rand, t int) []float32 { return g.vector(r, t) }

func (g *Generator) vector(r *rand.Rand, topic int) []float32 {
	noise := randomUnit(r, g.Dim)
	v := make([]float32, g.Dim)
	var norm float64
	for i := range v {
		v[i] = 0.7*g.centroids[topic][i] + 0.3*noise[i]
		norm += float64(v[i]) * float64(v[i])
	}
	scale := float32(1 / math.Sqrt(norm))
	for i := range v {
		v[i] *= scale
	}
	return v
}

func (g *Generator) sentence(r *rand.Rand, topics []string, lang, noun string) string {
	topic := strings.ReplaceAll(topics[r.IntN(len(topics))], "-", " ")
	adj := adjectives[zipfIndex(r, len(adjectives), 1.05)]
	switch r.IntN(4) {
	case 0:
		return fmt.Sprintf("A %s %s for %s.", adj, noun, topic)
	case 1:
		if lang == "" {
			lang = "any language"
		}
		return fmt.Sprintf("%s %s %s written in %s.", capitalize(adj), topic, noun, lang)
	case 2:
		return fmt.Sprintf("%s to %s %s with %s.", capitalize(noun), verbs[zipfIndex(r, len(verbs), 1.05)],
			objects[zipfIndex(r, len(objects), 1.05)], topic)
	default:
		return fmt.Sprintf("The %s %s you need to %s %s.", adj, topic, verbs[r.IntN(len(verbs))], objects[r.IntN(len(objects))])
	}
}

func word(r *rand.Rand, n int) string {
	var b strings.Builder
	for range n {
		b.WriteString(syllables[r.IntN(len(syllables))])
	}
	return b.String()
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// Languages and Licenses expose the value sets (for query generation).
func Languages() []string { return values(languages) }
func Licenses() []string  { return values(licenses) }

func values(ws []weighted) []string {
	out := make([]string, 0, len(ws))
	for _, w := range ws {
		if w.value != "" {
			out = append(out, w.value)
		}
	}
	return out
}
