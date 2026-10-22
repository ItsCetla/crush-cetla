package export

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/netlog"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/stretchr/testify/require"
)

const (
	fixtureHome = "/home/alice"
	fixtureWD   = "/home/alice/src/demo"
	fixtureT0   = int64(1792065600) // 2026-10-14 12:00 UTC.
)

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

// fixture is a small session that exercises every kind of block.
func fixture(t *testing.T) Data {
	t.Helper()

	oldMain := "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n"
	newMain := "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n"

	var longOut strings.Builder
	for i := 1; i <= 60; i++ {
		longOut.WriteString("line ")
		longOut.WriteString(strings.Repeat("x", i%7))
		longOut.WriteString("\n")
	}

	msgs := []message.Message{
		{
			ID: "m1", Role: message.User, CreatedAt: fixtureT0,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Make `main.go` say hello. My key is sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123 if you need it."},
				message.BinaryContent{Path: fixtureHome + "/Desktop/screenshot.png", MIMEType: "image/png", Data: []byte{0x89}},
			},
		},
		{
			ID: "m2", Role: message.Assistant, Model: "claude-sonnet-4-5", Provider: "anthropic", CreatedAt: fixtureT0 + 5,
			Parts: []message.ContentPart{
				message.ReasoningContent{Thinking: "The user wants `fmt.Println`. I should look at the file first."},
				message.TextContent{Text: "Let me look at the file.\n\n![diagram](https://example.com/tracker.png) <script>alert(1)</script>"},
				message.ToolCall{ID: "c1", Name: "view", Input: mustJSON(t, map[string]any{"file_path": fixtureWD + "/main.go"}), Finished: true},
				message.ToolCall{ID: "c2", Name: "bash", Input: mustJSON(t, map[string]any{"command": "env | grep API", "description": "Check keys"}), Finished: true},
				message.Finish{Reason: message.FinishReasonToolUse},
			},
		},
		{
			ID: "m3", Role: message.Tool, CreatedAt: fixtureT0 + 6,
			Parts: []message.ContentPart{
				message.ToolResult{ToolCallID: "c1", Name: "view", Content: longOut.String()},
				message.ToolResult{ToolCallID: "c2", Name: "bash", Content: "OPENAI_API_KEY=abc123def456ghi789\nMAX_TOKENS=4096\nAuthorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U"},
			},
		},
		{
			ID: "m4", Role: message.Assistant, Model: "claude-sonnet-4-5", Provider: "anthropic", CreatedAt: fixtureT0 + 20,
			Parts: []message.ContentPart{
				message.ToolCall{ID: "c3", Name: "edit", Input: mustJSON(t, map[string]any{
					"file_path":  fixtureWD + "/main.go",
					"old_string": "println(\"hi\")",
					"new_string": "fmt.Println(\"hello\")",
				}), Finished: true},
				message.ToolCall{ID: "c4", Name: "bash", Input: mustJSON(t, map[string]any{"command": "go run ."}), Finished: true},
				message.ToolCall{ID: "c5", Name: "grep", Input: mustJSON(t, map[string]any{"pattern": "TODO"}), Finished: false},
				message.Finish{Reason: message.FinishReasonToolUse},
			},
		},
		{
			ID: "m5", Role: message.Tool, CreatedAt: fixtureT0 + 21,
			Parts: []message.ContentPart{
				message.ToolResult{ToolCallID: "c3", Name: "edit", Content: "<result>\nContent replaced in file: " + fixtureWD + "/main.go\n</result>", Metadata: mustJSON(t, map[string]any{
					"additions": 3, "removals": 1, "old_content": oldMain, "new_content": newMain,
				})},
				message.ToolResult{ToolCallID: "c4", Name: "bash", Content: "exit status 2\n./main.go:3:8: \"fmt\" imported and not used", IsError: true},
			},
		},
		{
			ID: "m6", Role: message.Assistant, Model: "gpt-5", Provider: "openai", CreatedAt: fixtureT0 + 40,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Done. `main.go` now prints **hello**:\n\n```go\nfmt.Println(\"hello\")\n```\n\n| file | change |\n| --- | --- |\n| main.go | +3 −1 |"},
				message.Finish{Reason: message.FinishReasonEndTurn},
			},
		},
		{
			ID: "m7", Role: message.User, CreatedAt: fixtureT0 + 60,
			Parts: []message.ContentPart{
				message.ShellCommand{Command: "ls " + fixtureHome + "/src/demo", Output: "go.mod\nmain.go\n", ExitCode: 0},
			},
		},
		{
			ID: "m8", Role: message.User, CreatedAt: fixtureT0 + 70,
			Parts: []message.ContentPart{
				message.TextContent{Text: "continue", Hidden: true},
			},
		},
		{
			ID: "m9", Role: message.Assistant, Model: "gpt-5", Provider: "openai", CreatedAt: fixtureT0 + 80,
			Parts: []message.ContentPart{
				message.TextContent{Text: "Starting on the tests"},
				message.Finish{Reason: message.FinishReasonCanceled, Message: "Request cancelled"},
			},
		},
		{
			ID: "m10", Role: message.Assistant, Model: "gpt-5", Provider: "openai", CreatedAt: fixtureT0 + 90, IsSummaryMessage: true,
			Parts: []message.ContentPart{
				message.TextContent{Text: "We changed `main.go` to print hello with `fmt`."},
			},
		},
	}

	return Data{
		Session: session.Session{
			ID:               "5d0c9b5e-1f39-4b7a-9d2e-1c1f0e6c2a10",
			Title:            "Make main.go say hello",
			PromptTokens:     18234,
			CompletionTokens: 1532,
			Cost:             0.0731,
			CreatedAt:        fixtureT0,
			UpdatedAt:        fixtureT0 + 90,
		},
		Messages: msgs,
		Files: []history.File{
			{Path: fixtureWD + "/main.go", Content: oldMain, Version: 0},
			{Path: fixtureWD + "/main.go", Content: newMain, Version: 1},
			{Path: fixtureWD + "/README.md", Content: "same\n", Version: 0},
		},
		WorkingDir: fixtureWD,
		Hosts: []netlog.Host{
			{Host: "api.anthropic.com", Purposes: []netlog.Purpose{netlog.PurposeProvider}, Requests: 3, BytesOut: 48213, BytesIn: 9120},
			{Host: "127.0.0.1:11434", Purposes: []netlog.Purpose{netlog.PurposeDiscover}, Local: true, Requests: 1, BytesOut: 120, BytesIn: 840},
		},
		ExportedAt: time.Unix(fixtureT0+3600, 0),
	}
}

func fixtureOptions() Options {
	return Options{Redact: true, HomeDir: fixtureHome, Location: time.UTC}
}

func render(t *testing.T, f Format, d Data, o Options) string {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, Render(&buf, f, d, o))
	return buf.String()
}

func TestRenderMarkdown(t *testing.T) {
	t.Parallel()
	golden.RequireEqual(t, render(t, FormatMarkdown, fixture(t), fixtureOptions()))
}

func TestRenderHTML(t *testing.T) {
	t.Parallel()
	golden.RequireEqual(t, render(t, FormatHTML, fixture(t), fixtureOptions()))
}

func TestRenderJSON(t *testing.T) {
	t.Parallel()
	golden.RequireEqual(t, render(t, FormatJSON, fixture(t), fixtureOptions()))
}

func TestRenderRedactsEveryFormat(t *testing.T) {
	t.Parallel()
	for _, f := range []Format{FormatMarkdown, FormatHTML, FormatJSON} {
		out := render(t, f, fixture(t), fixtureOptions())
		require.NotContains(t, out, "sk-ant-api03", f)
		require.NotContains(t, out, "abc123def456ghi789", f)
		require.NotContains(t, out, "eyJhbGciOiJIUzI1NiJ9", f)
		require.NotContains(t, out, fixtureHome, f)
		require.Contains(t, out, "MAX_TOKENS=4096", f)
	}
}

func TestRenderNoRedact(t *testing.T) {
	t.Parallel()
	out := render(t, FormatJSON, fixture(t), Options{HomeDir: fixtureHome, Location: time.UTC})
	require.Contains(t, out, "sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123")
	require.Contains(t, out, "OPENAI_API_KEY=abc123def456ghi789")
	require.Contains(t, out, fixtureWD+"/main.go")
	require.Contains(t, out, `"redacted": false`)
}

func TestRenderHTMLIsSelfContained(t *testing.T) {
	t.Parallel()
	out := render(t, FormatHTML, fixture(t), fixtureOptions())
	require.NotContains(t, out, "<script")
	require.NotContains(t, out, "<img")
	require.NotContains(t, out, "<link")
	require.NotContains(t, out, `src="http`)
	require.Contains(t, out, "&lt;script&gt;")
	require.Contains(t, out, "[image: diagram]")
	require.Contains(t, out, "prefers-color-scheme: dark")
}

func TestBuildPairsToolResults(t *testing.T) {
	t.Parallel()
	doc := Build(fixture(t), fixtureOptions())

	var tools []*ToolCall
	for _, turn := range doc.Turns {
		require.NotEqual(t, "", string(turn.Role))
		for _, b := range turn.Blocks {
			if b.Tool != nil {
				tools = append(tools, b.Tool)
			}
		}
	}
	require.Len(t, tools, 5)
	require.Equal(t, "main.go", tools[0].Summary)
	require.Equal(t, "main.go", tools[2].DiffPath)
	require.Equal(t, 3, tools[2].Additions)
	require.Equal(t, 1, tools[2].Removals)
	require.True(t, tools[3].IsError)
	require.True(t, tools[4].NoResult)

	require.Equal(t, []string{"claude-sonnet-4-5", "gpt-5"}, doc.Models)
	require.Equal(t, "demo", doc.Project)
	require.Equal(t, []FileChange{{Path: "main.go", Additions: 3, Removals: 1, Diff: doc.Files[0].Diff}}, doc.Files)

	// The hidden continuation prompt is not a turn of its own.
	for _, turn := range doc.Turns {
		for _, b := range turn.Blocks {
			require.NotEqual(t, "continue", b.Text)
		}
	}
}

func TestBuildRedactsSummaryBeforeShortening(t *testing.T) {
	t.Parallel()

	// The token straddles the 120-rune summary limit, so shortening
	// first would leave a stub too short for the patterns to catch.
	cmd := strings.Repeat("x", 106) + " sk-abcdefghijklmnopqrstuvwx"
	d := Data{
		Session: session.Session{ID: "s", CreatedAt: fixtureT0},
		Messages: []message.Message{{
			ID: "m", Role: message.Assistant, CreatedAt: fixtureT0,
			Parts: []message.ContentPart{
				message.ToolCall{ID: "c", Name: "bash", Input: mustJSON(t, map[string]any{"command": cmd}), Finished: true},
			},
		}},
	}
	doc := Build(d, fixtureOptions())
	require.Len(t, doc.Turns, 1)
	tool := doc.Turns[0].Blocks[0].Tool
	require.NotNil(t, tool)
	require.NotContains(t, tool.Summary, "sk-")
	require.Contains(t, tool.Summary, Redacted)
}

func TestBuildFilesFromToolsWithoutHistory(t *testing.T) {
	t.Parallel()
	d := fixture(t)
	d.Files = nil
	doc := Build(d, fixtureOptions())
	require.Equal(t, []FileChange{{Path: "main.go", Additions: 3, Removals: 1}}, doc.Files)
}

func TestTruncate(t *testing.T) {
	t.Parallel()

	out, more := truncate("a\nb\nc\n", 5, 100)
	require.Equal(t, "a\nb\nc", out)
	require.Empty(t, more)

	out, more = truncate("1\n2\n3\n4\n5\n6", 4, 100)
	require.Equal(t, "1\n2\n3\n4", out)
	require.Equal(t, "… 2 more lines not shown", more)

	out, more = truncate("héllo wörld", 10, 2)
	require.Equal(t, "h", out)
	require.NotEmpty(t, more)
}

func TestParseFormat(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]Format{
		"":         FormatMarkdown,
		"md":       FormatMarkdown,
		"Markdown": FormatMarkdown,
		".html":    FormatHTML,
		"json":     FormatJSON,
	} {
		got, err := ParseFormat(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}
	_, err := ParseFormat("pdf")
	require.Error(t, err)
}

func TestFileName(t *testing.T) {
	t.Parallel()
	s := session.Session{ID: "5d0c9b5e-1f39-4b7a-9d2e-1c1f0e6c2a10", Title: "Fix the flaky test (again)!"}
	name := FileName(s, FormatHTML)
	require.True(t, strings.HasPrefix(name, "fix-the-flaky-test-again-"), name)
	require.True(t, strings.HasSuffix(name, ".html"), name)

	require.True(t, strings.HasPrefix(FileName(session.Session{ID: "x"}, FormatMarkdown), "session-"))
}
