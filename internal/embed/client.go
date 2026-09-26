// Package embed calls the ai-worker to turn text into dense vectors.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Dim is the vector size of the ai-worker model (all-MiniLM-L6-v2).
const Dim = 384

// MaxBatch is the largest number of texts the ai-worker accepts per request.
const MaxBatch = 256

type Client struct {
	baseURL string
	http    *http.Client
}

func NewClient(baseURL string) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: 60 * time.Second}}
}

// Embed returns one vector per text, batching requests as needed.
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += MaxBatch {
		end := min(start+MaxBatch, len(texts))
		vectors, err := c.embedBatch(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, vectors...)
	}
	return out, nil
}

func (c *Client) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(map[string]any{"texts": texts})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embed: status %d: %.300s", resp.StatusCode, data)
	}
	var res struct {
		Dim     int         `json:"dim"`
		Vectors [][]float32 `json:"vectors"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	if res.Dim != Dim || len(res.Vectors) != len(texts) {
		return nil, fmt.Errorf("embed: got %d vectors of dim %d for %d texts, want dim %d", len(res.Vectors), res.Dim, len(texts), Dim)
	}
	return res.Vectors, nil
}

// Ping checks that the ai-worker is up.
func (c *Client) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("embed: health status %d", resp.StatusCode)
	}
	return nil
}
