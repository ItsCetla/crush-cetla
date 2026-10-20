package chat

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestArtifactToolDisplay(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]string{
		"artifact":                             "Artifact",
		"artifact_data":                        "Artifact Data",
		"artifact_comments":                    "Artifact Comments",
		"mcp_tada-artifacts_artifact":          "Artifact",
		"mcp_tada-artifacts_artifact_data":     "Artifact Data",
		"mcp_tada-artifacts_artifact_comments": "Artifact Comments",
	} {
		got, ok := artifactToolDisplay(name)
		require.True(t, ok, name)
		require.Equal(t, want, got)
		require.Equal(t, want, prettifyToolName(name))
	}
	_, ok := artifactToolDisplay("mcp_other_artifact")
	require.False(t, ok)
}

func TestArtifactHeaderParamsSkipContent(t *testing.T) {
	t.Parallel()
	p := artifactCallParams{Title: "Q3 Revenue", ID: "abc123def"}
	require.Equal(t, []string{"publish", "title", "Q3 Revenue", "id", "abc123def"}, p.headerParams())

	p = artifactCallParams{Action: "set", ID: "abc123def", Collection: "rows", DocID: "a"}
	require.Equal(t, []string{"set", "id", "abc123def", "collection", "rows", "doc", "a"}, p.headerParams())
}
