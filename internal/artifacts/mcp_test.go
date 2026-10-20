package artifacts

import (
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

func TestMCPServerTools(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t, false)
	svc.OpenOnCreate = false
	ctx := t.Context()

	serverT, clientT := mcp.NewInMemoryTransports()
	ss, err := NewMCPServer(svc).Connect(ctx, serverT, nil)
	require.NoError(t, err)
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	require.NoError(t, err)
	defer cs.Close()

	require.Contains(t, cs.InitializeResult().Instructions, "artifact")

	tools, err := cs.ListTools(ctx, nil)
	require.NoError(t, err)
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
	}
	require.ElementsMatch(t, []string{"artifact", "artifact_data", "artifact_comments"}, names)

	textOf := func(r *mcp.CallToolResult) string {
		var b strings.Builder
		for _, c := range r.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				b.WriteString(tc.Text)
			}
		}
		return b.String()
	}

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "artifact", Arguments: map[string]any{"content": fullPage}})
	require.NoError(t, err)
	require.False(t, res.IsError, textOf(res))
	out := textOf(res)
	require.Contains(t, out, "Published \"Q3 Revenue\"")

	var id string
	for line := range strings.SplitSeq(out, "\n") {
		if v, ok := strings.CutPrefix(line, "id: "); ok {
			id = v
		}
	}
	require.NotEmpty(t, id)

	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "artifact_data", Arguments: map[string]any{
		"id": id, "action": "set", "collection": "rows", "data": map[string]any{"x": 1},
	}})
	require.NoError(t, err)
	require.False(t, res.IsError, textOf(res))

	// Errors come back as tool errors, not protocol errors.
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "artifact", Arguments: map[string]any{"action": "read", "id": "nosuchid1"}})
	require.NoError(t, err)
	require.True(t, res.IsError)
}

func TestToolSchemaKeepsMaps(t *testing.T) {
	t.Parallel()
	props, required := ToolSchema[DataParams]()
	require.ElementsMatch(t, []string{"id", "action"}, required)
	data, ok := props["data"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "object", data["type"])
	require.NotEmpty(t, data["description"])

	props, _ = ToolSchema[ArtifactParams]()
	files, ok := props["files"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "object", files["type"])
}
