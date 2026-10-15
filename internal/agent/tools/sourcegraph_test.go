package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/stretchr/testify/require"
)

func TestFormatSourcegraphResults(t *testing.T) {
	t.Parallel()

	result := map[string]any{
		"data": map[string]any{
			"search": map[string]any{
				"results": map[string]any{
					"matchCount":  float64(2),
					"resultCount": float64(1),
					"limitHit":    true,
					"results": []any{
						map[string]any{
							"__typename": "FileMatch",
							"repository": map[string]any{"name": "owner/repo"},
							"file": map[string]any{
								"path":    "main.go",
								"url":     "https://example.com/owner/repo/main.go",
								"content": "package main\n\nfunc main() {\n\tprintln(\"match\")\n}\n",
							},
							"lineMatches": []any{
								map[string]any{
									"lineNumber": float64(4),
									"preview":    "\tprintln(\"match\")",
								},
							},
						},
					},
				},
			},
		},
	}

	got, err := formatSourcegraphResults(result, 1, 10)
	require.NoError(t, err)
	require.Contains(t, got, "# Sourcegraph Search Results")
	require.Contains(t, got, "Found 2 matches across 1 results")
	require.Contains(t, got, "(Result limit reached, try a more specific query)")
	require.Contains(t, got, "## Result 1: owner/repo/main.go")
	require.Contains(t, got, "URL: https://example.com/owner/repo/main.go")
	require.Contains(t, got, "3| func main() {")
	require.Contains(t, got, "4|  \tprintln(\"match\")")
	require.Contains(t, got, "5| }")
}

func TestFormatSourcegraphResultsRespectsCount(t *testing.T) {
	t.Parallel()

	result := map[string]any{
		"data": map[string]any{
			"search": map[string]any{
				"results": map[string]any{
					"results": []any{
						map[string]any{
							"__typename": "FileMatch",
							"repository": map[string]any{"name": "owner/repo"},
							"file":       map[string]any{"path": "first.go"},
						},
						map[string]any{
							"__typename": "FileMatch",
							"repository": map[string]any{"name": "owner/repo"},
							"file":       map[string]any{"path": "second.go"},
						},
					},
				},
			},
		},
	}

	got, err := formatSourcegraphResults(result, 1, 1)
	require.NoError(t, err)
	require.Contains(t, got, "owner/repo/first.go")
	require.NotContains(t, got, "owner/repo/second.go")
}

func TestFormatSourcegraphResultsErrorsAndNoResults(t *testing.T) {
	t.Parallel()

	errorResult := map[string]any{
		"errors": []any{
			map[string]any{"message": "bad query"},
			map[string]any{"message": "timeout"},
		},
	}
	got, err := formatSourcegraphResults(errorResult, 1, 10)
	require.NoError(t, err)
	require.Equal(t, "## Sourcegraph API Error\n\n- bad query\n- timeout\n", got)

	noResult := map[string]any{
		"data": map[string]any{
			"search": map[string]any{
				"results": map[string]any{
					"matchCount":  float64(0),
					"resultCount": float64(0),
					"results":     []any{},
				},
			},
		},
	}
	got, err = formatSourcegraphResults(noResult, 1, 10)
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(got, "No results found. Try a different query.\n"))
}

// recordingPermissions is a [permission.Service] that answers every request
// with granted and remembers what was asked.
type recordingPermissions struct {
	permission.Service
	granted  bool
	requests []permission.CreatePermissionRequest
}

func (r *recordingPermissions) Request(_ context.Context, req permission.CreatePermissionRequest) (bool, error) {
	r.requests = append(r.requests, req)
	return r.granted, nil
}

// countingTransport stands in for sourcegraph.com and records the requests
// that reach it.
type countingTransport struct {
	urls []string
}

func (c *countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c.urls = append(c.urls, req.URL.String())
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"data":{"search":{"results":{"matchCount":0,"resultCount":0,"results":[]}}}}`)),
		Request:    req,
	}, nil
}

func runSourcegraphTool(t *testing.T, tool fantasy.AgentTool, query string) fantasy.ToolResponse {
	t.Helper()

	input, err := json.Marshal(SourcegraphParams{Query: query})
	require.NoError(t, err)

	ctx := context.WithValue(t.Context(), SessionIDContextKey, "test-session")
	resp, err := tool.Run(ctx, fantasy.ToolCall{
		ID:    "test-call",
		Name:  SourcegraphToolName,
		Input: string(input),
	})
	require.NoError(t, err)
	return resp
}

func TestSourcegraphToolAsksPermission(t *testing.T) {
	t.Parallel()

	t.Run("denied requests never leave the machine", func(t *testing.T) {
		t.Parallel()

		permissions := &recordingPermissions{granted: false}
		transport := &countingTransport{}
		tool := NewSourcegraphTool(permissions, "/work", &http.Client{Transport: transport})

		resp := runSourcegraphTool(t, tool, "func main lang:go")

		require.True(t, resp.IsError)
		require.True(t, resp.StopTurn)
		require.Empty(t, transport.urls, "no request is sent without permission")

		require.Len(t, permissions.requests, 1)
		req := permissions.requests[0]
		require.Equal(t, "test-session", req.SessionID)
		require.Equal(t, "/work", req.Path)
		require.Equal(t, "test-call", req.ToolCallID)
		require.Equal(t, SourcegraphToolName, req.ToolName)
		require.Equal(t, "search", req.Action)
		require.Contains(t, req.Description, "sourcegraph.com")
		require.Equal(t, SourcegraphPermissionsParams{
			Query:         "func main lang:go",
			Count:         10,
			ContextWindow: 10,
		}, req.Params)
	})

	t.Run("granted requests are sent", func(t *testing.T) {
		t.Parallel()

		permissions := &recordingPermissions{granted: true}
		transport := &countingTransport{}
		tool := NewSourcegraphTool(permissions, "/work", &http.Client{Transport: transport})

		resp := runSourcegraphTool(t, tool, "func main lang:go")

		require.False(t, resp.IsError, resp.Content)
		require.Len(t, permissions.requests, 1)
		require.Equal(t, []string{sourcegraphAPIURL}, transport.urls)
	})
}
