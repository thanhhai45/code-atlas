package search

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestWaitForGreenKeepsWaitingOnTimeout(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			// What Elasticsearch returns when wait_for_status times out.
			w.WriteHeader(http.StatusRequestTimeout)
			_, _ = w.Write([]byte(`{"status":"yellow","timed_out":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"green","timed_out":false}`))
	}))
	defer srv.Close()
	if err := NewClient(srv.URL, "a").WaitForGreen(context.Background(), "idx"); err != nil {
		t.Fatalf("a health timeout must not be an error: %v", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("expected 3 health calls, got %d", calls.Load())
	}
}
