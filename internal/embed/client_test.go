package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func fakeWorker(t *testing.T, dim int, calls *[]int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Texts []string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		*calls = append(*calls, len(req.Texts))
		vectors := make([][]float32, len(req.Texts))
		for i := range vectors {
			vectors[i] = make([]float32, dim)
			vectors[i][0] = float32(len(req.Texts[i]))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"dim": dim, "vectors": vectors})
	}))
}

func TestEmbedBatchesAndPreservesOrder(t *testing.T) {
	var calls []int
	srv := fakeWorker(t, Dim, &calls)
	defer srv.Close()

	texts := make([]string, MaxBatch+10)
	for i := range texts {
		texts[i] = string(make([]byte, i%7+1))
	}
	vectors, err := NewClient(srv.URL).Embed(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0] != MaxBatch || calls[1] != 10 {
		t.Errorf("batches = %v", calls)
	}
	for i, v := range vectors {
		if int(v[0]) != len(texts[i]) {
			t.Fatalf("vector %d out of order", i)
		}
	}
}

func TestEmbedRejectsWrongDimension(t *testing.T) {
	var calls []int
	srv := fakeWorker(t, 8, &calls)
	defer srv.Close()
	if _, err := NewClient(srv.URL).Embed(context.Background(), []string{"x"}); err == nil {
		t.Fatal("expected a dimension error")
	}
}
