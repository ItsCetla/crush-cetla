// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): features/magic-context/memory/embedding-openai.ts.

// OpenAI-compatible embeddings client, ported from cortexkit/magic-context
// (MIT) memory/embedding-openai.ts.

package magiccontext

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// NewHTTPEmbedder returns an Embedder that POSTs to `${endpoint}/embeddings`
// (OpenAI-compatible: OpenAI, Ollama, LM Studio, vLLM...). It returns nil
// when endpoint is empty, which turns embeddings off. Redirects are refused
// (SSRF guard). It only touches the network when called.
func NewHTTPEmbedder(endpoint, model, apiKey string) Embedder {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if endpoint == "" {
		return nil
	}
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("magiccontext: embeddings endpoint redirects are refused")
		},
	}
	url := endpoint + "/embeddings"
	return func(ctx context.Context, texts []string) ([][]float32, error) {
		if len(texts) == 0 {
			return nil, nil
		}
		body, err := json.Marshal(map[string]any{"model": model, "input": texts})
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			msg := string(raw)
			if len(msg) > 300 {
				msg = msg[:300]
			}
			return nil, fmt.Errorf("magiccontext: embeddings HTTP %d: %s", resp.StatusCode, msg)
		}
		var parsed struct {
			Model string `json:"model"`
			Data  []struct {
				Index     int       `json:"index"`
				Embedding []float32 `json:"embedding"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return nil, fmt.Errorf("magiccontext: embeddings response: %w", err)
		}
		out := make([][]float32, len(texts))
		for i, d := range parsed.Data {
			idx := d.Index
			if idx < 0 || idx >= len(texts) {
				idx = i
			}
			if idx < len(out) {
				out[idx] = d.Embedding
			}
		}
		for i, v := range out {
			if len(v) == 0 {
				return nil, fmt.Errorf("magiccontext: embeddings response missing vector %d", i)
			}
		}
		return out, nil
	}
}

// encodeEmbedding packs a vector as little-endian float32.
func encodeEmbedding(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

// decodeEmbedding unpacks a little-endian float32 blob.
func decodeEmbedding(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

// cosineSimilarity returns cos(a,b), 0 for empty/mismatched vectors.
func cosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
