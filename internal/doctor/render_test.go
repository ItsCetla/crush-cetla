package doctor

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func sampleReport() Report {
	return Report{
		Version: "v1.2.3",
		OK:      1,
		Warn:    1,
		Results: []Result{
			{ID: "tools", Name: "Tools", Status: StatusOK, Summary: "git", Items: []Item{
				{Status: StatusOK, Text: "git: /usr/bin/git"},
			}},
			{ID: "providers", Name: "Providers", Status: StatusWarn, Summary: "openai", Items: []Item{
				{Status: StatusWarn, Text: "openai: no API key", Hint: "Set $OPENAI_API_KEY."},
			}},
		},
	}
}

func TestWriteText(t *testing.T) {
	t.Parallel()

	t.Run("quiet shows only what needs attention", func(t *testing.T) {
		t.Parallel()
		var b bytes.Buffer
		require.NoError(t, WriteText(&b, sampleReport(), PlainStyles(), false))
		require.Equal(t, `Ta-Da doctor v1.2.3

  [ok]   Tools      git
  [warn] Providers  openai
           [warn] openai: no API key
                  -> Set $OPENAI_API_KEY.

1 ok, 1 warning.
Run `+"`tada doctor -v`"+` to see every finding.
`, b.String())
	})

	t.Run("verbose shows everything", func(t *testing.T) {
		t.Parallel()
		var b bytes.Buffer
		require.NoError(t, WriteText(&b, sampleReport(), PlainStyles(), true))
		require.Contains(t, b.String(), "           [ok]   git: /usr/bin/git\n")
		require.NotContains(t, b.String(), "tada doctor -v")
	})
}

func TestWriteJSON(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	require.NoError(t, WriteJSON(&b, sampleReport()))

	var got map[string]any
	require.NoError(t, json.Unmarshal(b.Bytes(), &got))
	require.Equal(t, "v1.2.3", got["version"])
	require.InDelta(t, 1, got["warn"], 0)
	results := got["results"].([]any)
	require.Len(t, results, 2)
	second := results[1].(map[string]any)
	require.Equal(t, "providers", second["id"])
	require.Equal(t, "warn", second["status"])
	item := second["items"].([]any)[0].(map[string]any)
	require.Equal(t, "Set $OPENAI_API_KEY.", item["hint"])

	// An empty report still has a results array.
	b.Reset()
	require.NoError(t, WriteJSON(&b, Report{}))
	require.Contains(t, b.String(), `"results": []`)
}
