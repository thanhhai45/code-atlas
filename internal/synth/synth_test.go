package synth

import (
	"math"
	"sort"
	"testing"
)

func TestDeterministicAndIndependent(t *testing.T) {
	a, b := New(42, 16), New(42, 16)
	if a.Repo(7).FullName != b.Repo(7).FullName || a.Repo(7).Embedding[3] != b.Repo(7).Embedding[3] {
		t.Fatal("same seed and index must produce the same repository")
	}
	// Generating other documents first must not change document 7.
	c := New(42, 16)
	for i := range 100 {
		c.Repo(i)
	}
	if c.Repo(7).Description != a.Repo(7).Description {
		t.Fatal("documents must not depend on generation order")
	}
	if New(43, 16).Repo(7).FullName == a.Repo(7).FullName {
		t.Error("different seeds should differ")
	}
}

func TestDistributions(t *testing.T) {
	g := New(1, 8)
	const n = 20_000
	stars := make([]int, n)
	topicCount := map[string]int{}
	langs := map[string]int{}
	for i := range n {
		r := g.Repo(i)
		if r.ID != int64(IDBase+i) || r.FullName == "" || r.Description == "" || len(r.Topics) == 0 {
			t.Fatalf("incomplete repository %+v", r)
		}
		if r.PushedAt.Before(r.CreatedAt) || r.PushedAt.After(g.Now) {
			t.Fatalf("pushed_at %v outside [%v, %v]", r.PushedAt, r.CreatedAt, g.Now)
		}
		var norm float64
		for _, x := range r.Embedding {
			norm += float64(x) * float64(x)
		}
		if math.Abs(norm-1) > 1e-3 {
			t.Fatalf("embedding not normalized: %f", norm)
		}
		stars[i] = r.Stars
		topicCount[r.Topics[0]]++
		langs[r.Language]++
	}
	sort.Ints(stars)
	median, p99 := stars[n/2], stars[n*99/100]
	// Power law: a small median and a long tail.
	if median > 20 || p99 < 200 {
		t.Errorf("stars distribution not heavy-tailed: median=%d p99=%d", median, p99)
	}
	if topicCount[Topics[0]] < 5*topicCount[Topics[50]] {
		t.Errorf("topics should be Zipf-skewed: %s=%d %s=%d", Topics[0], topicCount[Topics[0]], Topics[50], topicCount[Topics[50]])
	}
	if langs["JavaScript"] < langs["Haskell"]*10 {
		t.Errorf("language weights not applied: %v", langs)
	}
}

func TestVectorsClusterByTopic(t *testing.T) {
	g := New(3, 64)
	var same, diff []float64
	for i := range 2000 {
		a, b := g.Repo(i), g.Repo(i+1)
		d := dot(a.Embedding, b.Embedding)
		if a.Topics[0] == b.Topics[0] {
			same = append(same, d)
		} else {
			diff = append(diff, d)
		}
	}
	if mean(same) < mean(diff)+0.2 {
		t.Errorf("same-topic vectors should be closer: same=%.2f diff=%.2f", mean(same), mean(diff))
	}
}

func dot(a, b []float32) float64 {
	s := 0.0
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

func mean(xs []float64) float64 {
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}
