package control

import (
	"context"

	"github.com/charmbracelet/crush/internal/version"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RunMissionMCP serves the mission tool for one session over stdio, for
// the Claude Code engine, until ctx ends or the client disconnects.
func RunMissionMCP(ctx context.Context, ms *Missions, sessionID string) error {
	s := mcp.NewServer(&mcp.Implementation{
		Name:    "tada-mission",
		Title:   "Ta-Da Missions",
		Version: version.Version,
	}, nil)
	no := false
	mcp.AddTool(s, &mcp.Tool{
		Name:        MissionToolName,
		Title:       "Mission",
		Description: MissionToolDescription,
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &no, OpenWorldHint: &no},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, p MissionParams) (*mcp.CallToolResult, any, error) {
		out, err := RunMissionTool(ctx, ms, sessionID, p)
		if err != nil {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}, IsError: true}, nil, nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: out}}}, nil, nil
	})
	return s.Run(ctx, &mcp.StdioTransport{})
}
