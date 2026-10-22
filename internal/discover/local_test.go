package discover

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/stretchr/testify/require"
)

func serve(t *testing.T, routes map[string]string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestProbeOllama(t *testing.T) {
	t.Parallel()

	root := serve(t, map[string]string{
		"/api/version": `{"version":"0.12.3"}`,
		"/api/tags": `{"models":[
			{"name":"qwen2.5-coder:7b","model":"qwen2.5-coder:7b","details":{"parameter_size":"7.6B"}},
			{"name":"nomic-embed-text:latest","details":{"parameter_size":"137M"}},
			{"model":"llama3.2:3b"}
		]}`,
	})

	got := Probe(context.Background(), []Target{{Kind: KindOllama, Root: root + "/"}}, time.Second)
	require.Len(t, got, 1)
	s := got[0]
	require.Equal(t, KindOllama, s.Kind)
	require.Equal(t, "Ollama", s.Name)
	require.Equal(t, "0.12.3", s.Version)
	require.Equal(t, root, s.Root)
	require.Equal(t, root+"/v1", s.BaseURL)
	require.Equal(t, catwalk.Type("ollama"), s.ProviderType)
	require.Equal(t, "ollama", s.ProviderID)
	require.Equal(t, []LocalModel{
		{ID: "qwen2.5-coder:7b", ParameterSize: "7.6B"},
		{ID: "llama3.2:3b"},
	}, s.Models)
}

func TestProbeOllamaWithoutVersionOrModels(t *testing.T) {
	t.Parallel()

	root := serve(t, map[string]string{"/api/tags": `{"models":[]}`})
	got := Probe(context.Background(), []Target{{Kind: KindOllama, Root: root}}, time.Second)
	require.Len(t, got, 1)
	require.Empty(t, got[0].Version)
	require.Empty(t, got[0].Models)
}

func TestProbeLMStudio(t *testing.T) {
	t.Parallel()

	root := serve(t, map[string]string{
		"/v1/models": `{"object":"list","data":[
			{"id":"qwen/qwen3-coder-30b","owned_by":"organization_owner"},
			{"id":"text-embedding-nomic-embed-text-v1.5"}
		]}`,
	})
	got := Probe(context.Background(), []Target{{Kind: KindLMStudio, Root: root}}, time.Second)
	require.Len(t, got, 1)
	s := got[0]
	require.Equal(t, KindLMStudio, s.Kind)
	require.Equal(t, "LM Studio", s.Name)
	require.Equal(t, root+"/v1", s.BaseURL)
	require.Equal(t, catwalk.Type("lmstudio"), s.ProviderType)
	require.Equal(t, []LocalModel{{ID: "qwen/qwen3-coder-30b"}}, s.Models)
}

func TestProbeLlamaCpp(t *testing.T) {
	t.Parallel()

	root := serve(t, map[string]string{
		"/v1/models": `{"data":[{"id":"gpt-oss-20b","owned_by":"llamacpp","meta":{"n_ctx":8192}}]}`,
	})
	got := Probe(context.Background(), []Target{{Kind: KindLlamaCpp, Root: root}}, time.Second)
	require.Len(t, got, 1)
	s := got[0]
	require.Equal(t, KindLlamaCpp, s.Kind)
	require.Equal(t, "llama.cpp", s.Name)
	// The llamacpp enricher appends /v1 itself.
	require.Equal(t, root, s.BaseURL)
	require.Equal(t, catwalk.Type("llamacpp"), s.ProviderType)
	require.Equal(t, []LocalModel{{ID: "gpt-oss-20b"}}, s.Models)
}

func TestProbeGenericOpenAICompat(t *testing.T) {
	t.Parallel()

	root := serve(t, map[string]string{
		"/v1/models": `{"data":[{"id":"mistral-7b","owned_by":"vllm"}]}`,
	})
	got := Probe(context.Background(), []Target{{Kind: KindLlamaCpp, Root: root}}, time.Second)
	require.Len(t, got, 1)
	s := got[0]
	require.Equal(t, KindOpenAICompat, s.Kind)
	require.Equal(t, root+"/v1", s.BaseURL)
	require.Equal(t, catwalk.TypeOpenAICompat, s.ProviderType)
	require.Equal(t, "local", s.ProviderID)
}

func TestProbeSkipsNonServers(t *testing.T) {
	t.Parallel()

	// A web app on the port that answers 404, one that answers JSON
	// without a model list, and a port with nothing listening.
	notFound := serve(t, map[string]string{})
	wrongShape := serve(t, map[string]string{
		"/api/tags":  `{"hello":"world"}`,
		"/v1/models": `{"hello":"world"}`,
	})
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()

	got := Probe(context.Background(), []Target{
		{Kind: KindOllama, Root: notFound},
		{Kind: KindOllama, Root: wrongShape},
		{Kind: KindLMStudio, Root: wrongShape},
		{Kind: KindLlamaCpp, Root: closedURL},
	}, time.Second)
	require.Empty(t, got)
}

func TestProbeTimesOutSlowServers(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	t.Cleanup(slow.Close)
	t.Cleanup(func() { close(release) })
	fast := serve(t, map[string]string{"/v1/models": `{"data":[{"id":"m"}]}`})

	start := time.Now()
	got := Probe(context.Background(), []Target{
		{Kind: KindOllama, Root: slow.URL},
		{Kind: KindLMStudio, Root: fast},
	}, 50*time.Millisecond)
	require.Less(t, time.Since(start), 2*time.Second)
	require.Len(t, got, 1)
	require.Equal(t, KindLMStudio, got[0].Kind)
}
