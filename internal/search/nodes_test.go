package search

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCluster answers requests per node host; a node is "down" (connection
// refused), "reset" (connection lost after the request was sent), "slow"
// (timeout), "busy" (503) or healthy.
type fakeCluster struct {
	mu    sync.Mutex
	state map[string]string
	calls map[string]int
}

func (f *fakeCluster) RoundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	host := r.URL.Host
	f.calls[host]++
	state := f.state[host]
	f.mu.Unlock()
	switch state {
	case "down":
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	case "reset":
		return nil, &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset by peer")}
	case "slow":
		return nil, &net.OpError{Op: "read", Net: "tcp", Err: timeoutErr{}}
	case "busy":
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"status":"green","node":"` + host + `"}`)), Request: r}, nil
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func newFakeClient(states map[string]string) (*Client, *fakeCluster, *time.Time) {
	f := &fakeCluster{state: states, calls: map[string]int{}}
	c := NewClient("http://a:9200, http://b:9200/,http://c:9200", "repos")
	c.http = &http.Client{Transport: f}
	now := time.Unix(1000, 0)
	c.pool.now = func() time.Time { return now }
	return c, f, &now
}

func (f *fakeCluster) count(host string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[host]
}

func TestClientRoundRobinsOverNodes(t *testing.T) {
	c, f, _ := newFakeClient(map[string]string{})
	for range 9 {
		if _, err := c.Ping(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	for _, h := range []string{"a:9200", "b:9200", "c:9200"} {
		if f.count(h) != 3 {
			t.Errorf("node %s got %d of 9 requests, want 3", h, f.count(h))
		}
	}
}

func TestClientSkipsDeadNodeUntilCooldownEnds(t *testing.T) {
	c, f, now := newFakeClient(map[string]string{"b:9200": "down"})
	for range 30 {
		if _, err := c.Ping(context.Background()); err != nil {
			t.Fatalf("a dead node must not fail requests: %v", err)
		}
	}
	if got := f.count("b:9200"); got != 1 {
		t.Fatalf("dead node tried %d times, want once before the cooldown", got)
	}
	*now = now.Add(deadCooldown + time.Second)
	f.mu.Lock()
	f.state["b:9200"] = ""
	f.mu.Unlock()
	for range 6 {
		_, _ = c.Ping(context.Background())
	}
	if got := f.count("b:9200"); got < 2 {
		t.Fatalf("node must be tried again after the cooldown, calls=%d", got)
	}
}

func TestClientTriesDeadNodesAsLastResort(t *testing.T) {
	c, f, _ := newFakeClient(map[string]string{"a:9200": "down", "b:9200": "down", "c:9200": "down"})
	if _, err := c.Ping(context.Background()); err == nil {
		t.Fatal("expected an error with every node down")
	}
	f.mu.Lock()
	f.state["c:9200"] = ""
	f.mu.Unlock()
	if _, err := c.Ping(context.Background()); err != nil {
		t.Fatalf("all nodes marked dead: they must still be tried: %v", err)
	}
}

func TestClientRetriesOnlySafeRequests(t *testing.T) {
	ctx := context.Background()
	// Connection lost after sending: a search is resent, a write is not.
	c, f, _ := newFakeClient(map[string]string{"a:9200": "reset", "b:9200": "reset", "c:9200": "reset"})
	_ = c.RawSearch(ctx, map[string]any{}, nil)
	if f.count("a:9200")+f.count("b:9200")+f.count("c:9200") != 3 {
		t.Errorf("a search must be tried on every node, calls=%v", f.calls)
	}
	c, f, _ = newFakeClient(map[string]string{"a:9200": "reset", "b:9200": "reset", "c:9200": "reset"})
	_ = c.UpdateSettings(ctx, "idx", map[string]any{"refresh_interval": "1s"})
	if total := f.count("a:9200") + f.count("b:9200") + f.count("c:9200"); total != 1 {
		t.Errorf("a write that may have been applied must not be resent, calls=%d", total)
	}
	// Refused connection: nothing was sent, so even a write moves on.
	c, f, _ = newFakeClient(map[string]string{"a:9200": "down", "b:9200": "down"})
	if err := c.UpdateSettings(ctx, "idx", map[string]any{"refresh_interval": "1s"}); err != nil {
		t.Errorf("a write that never reached a node must go to the next one: %v", err)
	}
	// A timeout is a slow node, not a dead one: not resent.
	c, f, _ = newFakeClient(map[string]string{"a:9200": "slow", "b:9200": "slow", "c:9200": "slow"})
	_ = c.RawSearch(ctx, map[string]any{}, nil)
	if total := f.count("a:9200") + f.count("b:9200") + f.count("c:9200"); total != 1 {
		t.Errorf("a timed-out search must not be resent, calls=%d", total)
	}
	// 503 from one node: a search moves on.
	c, _, _ = newFakeClient(map[string]string{"a:9200": "busy", "b:9200": "busy"})
	var res map[string]any
	if err := c.RawSearch(ctx, map[string]any{}, &res); err != nil || res["node"] != "c:9200" {
		t.Errorf("a search must move past 503 answers: res=%v err=%v", res, err)
	}
}

func TestReadOnly(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		want         bool
	}{
		{"GET", "/_cluster/health", true},
		{"POST", "/repos/_search", true},
		{"POST", "/repos/_search?typed_keys=true", true},
		{"POST", "/repos/_rank_eval", true},
		{"POST", "/_bulk", false},
		{"POST", "/_aliases", false},
		{"PUT", "/idx/_settings", false},
		{"POST", "/idx/_forcemerge?max_num_segments=1", false},
	} {
		if got := readOnly(tc.method, tc.path); got != tc.want {
			t.Errorf("readOnly(%s %s) = %v, want %v", tc.method, tc.path, got, tc.want)
		}
	}
}
