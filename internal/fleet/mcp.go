package fleet

import (
	"context"

	"github.com/charmbracelet/crush/internal/version"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPServerName is the server name the Claude Code engine registers, so
// its tools show up as mcp__tada-machines__<tool>.
const MCPServerName = "tada-machines"

// RunMCP serves the machines and machine_ask tools over stdio for the
// Claude Code engine, until ctx ends or the client disconnects.
//
// The server doesn't ask for permission itself: the Claude Code CLI routes
// every MCP call through Ta-Da's permission dialog, which lets reads and
// machine_ask through and asks before a machine is saved or removed.
func RunMCP(ctx context.Context, store Store, workingDir string) error {
	s := mcp.NewServer(&mcp.Implementation{
		Name:    MCPServerName,
		Title:   "Ta-Da Machines",
		Version: version.Version,
	}, nil)
	no, yes := false, true
	mcp.AddTool(s, &mcp.Tool{
		Name:        ToolName,
		Title:       "Machines",
		Description: ToolDescription,
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &yes, OpenWorldHint: &no},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, p Params) (*mcp.CallToolResult, any, error) {
		out, err := RunTool(ctx, store, nil, p)
		return mcpResult(out, err), nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        AskToolName,
		Title:       "Ask a machine",
		Description: AskToolDescription,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &yes},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, p AskParams) (*mcp.CallToolResult, any, error) {
		out, err := Ask(ctx, store.Config(), nil, workingDir, p)
		return mcpResult(out, err), nil, nil
	})
	return s.Run(ctx, &mcp.StdioTransport{})
}

func mcpResult(out string, err error) *mcp.CallToolResult {
	if err != nil {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}, IsError: true}
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: out}}}
}
