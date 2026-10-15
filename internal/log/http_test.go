package log

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestHTTPRoundTripLogger(t *testing.T) {
	// Create a test server that returns a 500 error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Custom-Header", "test-value")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error": "Internal server error", "code": 500}`))
	}))
	defer server.Close()

	// Create HTTP client with logging
	client := NewHTTPClient()

	// Make a request
	req, err := http.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		server.URL,
		strings.NewReader(`{"test": "data"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret-token")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Verify response
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("Expected status code 500, got %d", resp.StatusCode)
	}
}

func TestFormatHeaders(t *testing.T) {
	headers := http.Header{
		"Content-Type":  []string{"application/json"},
		"Authorization": []string{"Bearer secret-token"},
		"X-API-Key":     []string{"api-key-123"},
		"User-Agent":    []string{"test-agent"},
	}

	formatted := formatHeaders(headers)

	// Check that sensitive headers are redacted
	if formatted["Authorization"][0] != "[REDACTED]" {
		t.Error("Authorization header should be redacted")
	}
	if formatted["X-API-Key"][0] != "[REDACTED]" {
		t.Error("X-API-Key header should be redacted")
	}

	// Check that non-sensitive headers are preserved
	if formatted["Content-Type"][0] != "application/json" {
		t.Error("Content-Type header should be preserved")
	}
	if formatted["User-Agent"][0] != "test-agent" {
		t.Error("User-Agent header should be preserved")
	}
}

func TestFormatHeadersRedactsCredentials(t *testing.T) {
	headers := http.Header{}
	for _, name := range []string{
		"Authorization",
		"Proxy-Authorization",
		"X-Api-Key",
		"Api-Key",
		"X-Goog-Api-Key",
		"Cookie",
		"Set-Cookie",
		"X-Session-Token",
		"X-Client-Secret",
	} {
		headers.Set(name, "sensitive")
	}
	headers.Set("Content-Type", "application/json")

	formatted := formatHeaders(headers)
	for name, values := range formatted {
		if name == "Content-Type" {
			if values[0] != "application/json" {
				t.Errorf("%s header should be preserved", name)
			}
			continue
		}
		if values[0] != "[REDACTED]" {
			t.Errorf("%s header should be redacted, got %q", name, values[0])
		}
	}
}

func TestRedactURL(t *testing.T) {
	tests := map[string]string{
		"https://example.com/v1/models?key=abc123&alt=sse": "https://example.com/v1/models?key=[REDACTED]&alt=sse",
		"https://example.com/v1/models?alt=sse&KEY=abc123": "https://example.com/v1/models?alt=sse&KEY=[REDACTED]",
		"https://user:pass@example.com/path":               "https://user:xxxxx@example.com/path",
		"https://example.com/path?monkey=1":                "https://example.com/path?monkey=1",
	}
	for raw, want := range tests {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := redactURL(u); got != want {
			t.Errorf("redactURL(%q) = %q, want %q", raw, got, want)
		}
	}

	errMsg := `Post "https://example.com/v1?key=abc123": dial tcp: connection refused`
	want := `Post "https://example.com/v1?key=[REDACTED]": dial tcp: connection refused`
	if got := redactKeyParam(errMsg); got != want {
		t.Errorf("redactKeyParam() = %q, want %q", got, want)
	}
}
