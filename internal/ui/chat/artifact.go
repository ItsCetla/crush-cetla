package chat

import (
	"encoding/json"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
)

// artifactMCPPrefix is the name Claude Code engine sessions give the
// artifact tools, after Ta-Da rewrites mcp__server__tool to
// mcp_server_tool.
const artifactMCPPrefix = "mcp_tada-artifacts_"

// artifactToolDisplay maps the artifact tool names, native and MCP, to the
// label shown in the chat. It reports false for any other tool.
func artifactToolDisplay(name string) (string, bool) {
	switch name {
	case tools.ArtifactToolName, artifactMCPPrefix + tools.ArtifactToolName:
		return "Artifact", true
	case tools.ArtifactDataToolName, artifactMCPPrefix + tools.ArtifactDataToolName:
		return "Artifact Data", true
	case tools.ArtifactCommentsToolName, artifactMCPPrefix + tools.ArtifactCommentsToolName:
		return "Artifact Comments", true
	}
	return "", false
}

// ArtifactToolMessageItem is a message item that represents a call to one
// of the artifact tools.
type ArtifactToolMessageItem struct {
	*baseToolMessageItem
}

var _ ToolMessageItem = (*ArtifactToolMessageItem)(nil)

// NewArtifactToolMessageItem creates a new [ArtifactToolMessageItem].
func NewArtifactToolMessageItem(
	sty *styles.Styles,
	toolCall message.ToolCall,
	result *message.ToolResult,
	canceled bool,
) ToolMessageItem {
	name, _ := artifactToolDisplay(toolCall.Name)
	return newBaseToolMessageItem(sty, toolCall, result, &ArtifactToolRenderContext{name: name}, canceled)
}

// ArtifactToolRenderContext renders artifact tool messages. It shows the
// action and what it acts on, never the page content itself.
type ArtifactToolRenderContext struct {
	name string
}

// artifactCallParams holds the fields of the three artifact tools that are
// worth showing in a header.
type artifactCallParams struct {
	Action     string `json:"action"`
	ID         string `json:"id"`
	Title      string `json:"title"`
	FilePath   string `json:"file_path"`
	Kind       string `json:"kind"`
	Path       string `json:"path"`
	Collection string `json:"collection"`
	DocID      string `json:"doc_id"`
	CommentID  string `json:"comment_id"`
}

// headerParams returns the main header value followed by key/value pairs.
func (p artifactCallParams) headerParams() []string {
	main := p.Action
	if main == "" {
		main = "publish"
	}
	out := []string{main}
	add := func(k, v string) {
		if v != "" {
			out = append(out, k, v)
		}
	}
	add("title", p.Title)
	add("id", p.ID)
	add("kind", p.Kind)
	add("file", p.FilePath)
	add("path", p.Path)
	add("collection", p.Collection)
	add("doc", p.DocID)
	add("comment", p.CommentID)
	return out
}

// RenderTool implements the [ToolRenderer] interface.
func (r *ArtifactToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	name := r.name
	if name == "" {
		name = "Artifact"
	}
	cappedWidth := cappedMessageWidth(width)
	if opts.IsPending() {
		return pendingTool(sty, name, opts.Anim, opts.Compact)
	}

	var params artifactCallParams
	_ = json.Unmarshal([]byte(opts.ToolCall.Input), &params)

	header := toolHeader(sty, opts.Status, name, cappedWidth, opts, params.headerParams()...)
	if opts.Compact {
		return header
	}

	if earlyState, ok := toolEarlyStateContent(sty, opts, cappedWidth); ok {
		return joinToolParts(header, earlyState)
	}

	if opts.HasEmptyResult() {
		return header
	}

	bodyWidth := cappedWidth - toolBodyLeftPaddingTotal
	body := sty.Tool.Body.Render(toolOutputPlainContent(sty, opts.Result.Content, bodyWidth, opts.ExpandedContent))
	return joinToolParts(header, body)
}
