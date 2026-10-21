package tools

import (
	"context"
	_ "embed"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/workdir"
)

//go:embed web_fetch.md.tpl
var webFetchDescriptionTmpl []byte

var webFetchDescriptionTpl = template.Must(
	template.New("webFetchDescription").
		Parse(string(webFetchDescriptionTmpl)),
)

// NewWebFetchTool creates a simple web fetch tool for sub-agents. Each fetch
// asks for permission because it makes an outbound request.
func NewWebFetchTool(permissions permission.Service, workingDir string, client *http.Client) fantasy.AgentTool {
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.MaxIdleConns = 100
		transport.MaxIdleConnsPerHost = 10
		transport.IdleConnTimeout = 90 * time.Second

		client = &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
		}
	}

	return fantasy.NewParallelAgentTool(
		WebFetchToolName,
		renderToolDescription(webFetchDescriptionTpl),
		func(ctx context.Context, params WebFetchParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			workingDir := workdir.From(ctx, workingDir)
			if params.URL == "" {
				return fantasy.NewTextErrorResponse("url is required"), nil
			}

			sessionID := GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.ToolResponse{}, fmt.Errorf("session ID is required for fetching a URL")
			}

			p, err := permissions.Request(
				ctx,
				permission.CreatePermissionRequest{
					SessionID:   sessionID,
					Path:        workingDir,
					ToolCallID:  call.ID,
					ToolName:    WebFetchToolName,
					Action:      "fetch",
					Description: fmt.Sprintf("Fetch content from URL: %s", params.URL),
					Params:      params,
				},
			)
			if err != nil {
				return fantasy.ToolResponse{}, err
			}
			if !p {
				return NewPermissionDeniedResponse(), nil
			}

			content, err := FetchURLAndConvert(ctx, client, params.URL)
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("Failed to fetch URL: %s", err)), nil
			}

			hasLargeContent := len(content) > LargeContentThreshold
			var result strings.Builder

			if hasLargeContent {
				tempFile, err := os.CreateTemp(workingDir, "page-*.md")
				if err != nil {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("Failed to create temporary file: %s", err)), nil
				}
				tempFilePath := tempFile.Name()

				if _, err := tempFile.WriteString(content); err != nil {
					_ = tempFile.Close() // Best effort close
					return fantasy.NewTextErrorResponse(fmt.Sprintf("Failed to write content to file: %s", err)), nil
				}
				if err := tempFile.Close(); err != nil {
					return fantasy.NewTextErrorResponse(fmt.Sprintf("Failed to close temporary file: %s", err)), nil
				}

				fmt.Fprintf(&result, "Fetched content from %s (large page)\n\n", params.URL)
				fmt.Fprintf(&result, "Content saved to: %s\n\n", tempFilePath)
				result.WriteString("Use the view and grep tools to analyze this file.")
			} else {
				fmt.Fprintf(&result, "Fetched content from %s:\n\n", params.URL)
				result.WriteString(content)
			}

			return fantasy.NewTextResponse(result.String()), nil
		},
	)
}
