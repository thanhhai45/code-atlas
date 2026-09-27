package main

import (
	"strings"
	"testing"

	"github.com/thanhhai45/code-atlas/internal/search"
)

func TestPlanLayoutKeepsCurrentRedundancy(t *testing.T) {
	cur := &search.IndexLayout{Shards: 3, Replicas: 1}
	for _, tc := range []struct {
		name             string
		current          *search.IndexLayout
		shards, replicas int
		want             search.IndexLayout
	}{
		{"copies the live index", cur, 0, -1, search.IndexLayout{Shards: 3, Replicas: 1}},
		{"flags override", cur, 5, 2, search.IndexLayout{Shards: 5, Replicas: 2}},
		{"replicas can be set to zero", cur, 0, 0, search.IndexLayout{Shards: 3, Replicas: 0}},
		{"no live index: 1 shard, 0 replicas", nil, 0, -1, search.IndexLayout{Shards: 1, Replicas: 0}},
	} {
		got, err := planLayout(tc.current, tc.shards, tc.replicas)
		if err != nil || got != tc.want {
			t.Errorf("%s: got %+v, %v; want %+v", tc.name, got, err, tc.want)
		}
	}
}

func TestCheckCounts(t *testing.T) {
	if err := checkCounts(100, 100, 105, 0.1); err != nil {
		t.Errorf("a small drop is allowed: %v", err)
	}
	if err := checkCounts(100, 100, 0, 0.1); err != nil {
		t.Errorf("an empty live index cannot shrink: %v", err)
	}
	if err := checkCounts(100, 99, 100, 0.1); err == nil || !strings.Contains(err.Error(), "99") {
		t.Errorf("documents lost in the load must block the swap: %v", err)
	}
	if err := checkCounts(50, 50, 100, 0.1); err == nil || !strings.Contains(err.Error(), "50% fewer") {
		t.Errorf("a new index half the size of the live one must block the swap: %v", err)
	}
	if err := checkCounts(50, 50, 100, 0.6); err != nil {
		t.Errorf("-max-shrink raises the limit: %v", err)
	}
}
