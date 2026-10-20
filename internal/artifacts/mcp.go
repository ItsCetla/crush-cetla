package artifacts

import (
	"context"
	"os"

	"github.com/charmbracelet/crush/internal/version"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const mcpInstructions = `Ta-Da artifacts: publish HTML pages, Markdown documents and slide decks to a local viewer on this machine (nothing leaves it). Use the artifact tool when a page would be clearer than terminal text or the user would use the page rather than only read it. Call artifact with action "guide" once before writing your first page; it holds the page contract and the window.tada runtime (a shared database, storage and comments). After publishing, give the user the link from the result.`

const (
	artifactToolDesc = `Publish and manage artifacts: HTML pages, Markdown documents and slide decks shown in a local viewer at a stable link. Actions: publish (default; no id creates a new artifact, an id publishes a new version), list, read, delete, open, pin, unpin, guide. Read the guide once before your first page. Prefer file_path over inline content for long pages, and give the user the link from the result.`
	dataToolDesc     = `Read and write an artifact's shared database, the same data its page reads through window.tada.db. Actions: get, list, query, set, update, delete, batch, collections. The open page sees writes live.`
	commentsToolDesc = `Read and answer comments the user left on an artifact in the viewer. Actions: list (default), reply, resolve, unresolve.`
)

func text(s string, isErr bool) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}, IsError: isErr}
}

func errResult(err error) (*mcp.CallToolResult, any, error) {
	return text(err.Error(), true), nil, nil
}

// NewMCPServer builds an MCP server exposing the artifact tools.
func NewMCPServer(svc *Service) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{
		Name:    "tada-artifacts",
		Title:   "Ta-Da Artifacts",
		Version: version.Version,
	}, &mcp.ServerOptions{Instructions: mcpInstructions})

	env := func() CallEnv {
		wd, _ := os.Getwd()
		return CallEnv{WorkingDir: wd, Project: wd}
	}
	yes, no := true, false

	mcp.AddTool(s, &mcp.Tool{
		Name:        "artifact",
		Title:       "Artifacts",
		Description: artifactToolDesc,
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &yes, OpenWorldHint: &no},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, p ArtifactParams) (*mcp.CallToolResult, any, error) {
		r, err := svc.Artifact(ctx, env(), p)
		if err != nil {
			return errResult(err)
		}
		return text(r.Text, false), nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "artifact_data",
		Title:       "Artifact database",
		Description: dataToolDesc,
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &yes, OpenWorldHint: &no},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, p DataParams) (*mcp.CallToolResult, any, error) {
		out, err := svc.Data(ctx, p)
		if err != nil {
			return errResult(err)
		}
		return text(out, false), nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "artifact_comments",
		Title:       "Artifact comments",
		Description: commentsToolDesc,
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &no, OpenWorldHint: &no},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, p CommentsParams) (*mcp.CallToolResult, any, error) {
		out, err := svc.Comments(ctx, p)
		if err != nil {
			return errResult(err)
		}
		return text(out, false), nil, nil
	})
	return s
}

// RunMCP serves the artifact tools over stdio until ctx ends or the
// client disconnects.
func RunMCP(ctx context.Context, svc *Service) error {
	return NewMCPServer(svc).Run(ctx, &mcp.StdioTransport{})
}
