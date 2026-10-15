package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

func runDownload(t *testing.T, url string) (fantasy.ToolResponse, string) {
	t.Helper()
	dir := t.TempDir()
	tool := NewDownloadTool(&mockPermissionService{}, dir, nil)
	input, err := json.Marshal(DownloadParams{URL: url, FilePath: "out.bin"})
	require.NoError(t, err)
	ctx := context.WithValue(t.Context(), SessionIDContextKey, "test-session")
	resp, err := tool.Run(ctx, fantasy.ToolCall{ID: "call", Name: DownloadToolName, Input: string(input)})
	require.NoError(t, err)
	return resp, filepath.Join(dir, "out.bin")
}

func TestDownloadSizeLimit(t *testing.T) {
	t.Parallel()

	t.Run("small body is saved", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "hello")
		}))
		defer srv.Close()

		resp, path := runDownload(t, srv.URL)
		require.False(t, resp.IsError, resp.Content)
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, "hello", string(data))
	})

	t.Run("oversized content length is rejected", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", strconv.Itoa(maxDownloadSize+1))
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		resp, path := runDownload(t, srv.URL)
		require.True(t, resp.IsError)
		require.Contains(t, resp.Content, "Download too large")
		require.NoFileExists(t, path)
	})

	t.Run("oversized streamed body is removed", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			// Flushing first forces chunked encoding, so there is no
			// Content-Length for the tool to check up front.
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			chunk := strings.Repeat("x", 1<<20)
			for range maxDownloadSize/len(chunk) + 1 {
				if _, err := io.WriteString(w, chunk); err != nil {
					return
				}
			}
		}))
		defer srv.Close()

		resp, path := runDownload(t, srv.URL)
		require.True(t, resp.IsError)
		require.Contains(t, resp.Content, "Download too large")
		require.NoFileExists(t, path)
	})
}
