package agent

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/claudecode"
	"github.com/charmbracelet/crush/internal/diff"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/session"
)

// ccToolName maps a Claude Code tool name to the Ta-Da tool whose card
// renders it. Unknown tools keep their name and get the generic card.
func ccToolName(name string) string {
	switch name {
	case "Bash":
		return tools.BashToolName
	case "Read":
		return tools.ViewToolName
	case "Edit":
		return tools.EditToolName
	case "Write":
		return tools.WriteToolName
	case "MultiEdit":
		return tools.MultiEditToolName
	case "Glob":
		return tools.GlobToolName
	case "Grep":
		return tools.GrepToolName
	case "LS":
		return tools.LSToolName
	case "TodoWrite":
		return tools.TodosToolName
	case "WebFetch":
		return tools.FetchToolName
	case "WebSearch":
		return tools.WebSearchToolName
	case "Task", "Agent":
		return AgentToolName
	case "AskUserQuestion":
		return tools.QuestionToolName
	}
	// Claude Code names MCP tools mcp__server__tool; Ta-Da's MCP card
	// expects mcp_server_tool.
	if rest, ok := strings.CutPrefix(name, "mcp__"); ok {
		if server, tool, ok := strings.Cut(rest, "__"); ok {
			return "mcp_" + server + "_" + tool
		}
	}
	return name
}

// ccToolInput rewrites a Claude Code tool input into the parameter shape
// the matching Ta-Da card reads. Unknown fields are kept.
func ccToolInput(ccName string, raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	if ccName == "AskUserQuestion" {
		if s, ok := ccAskToolInput(raw); ok {
			return s
		}
	}
	var in map[string]any
	if json.Unmarshal(raw, &in) != nil || in == nil {
		return string(raw)
	}
	switch ccName {
	case "Read":
		// Claude Code offsets are 1-based line numbers; Ta-Da's are 0-based.
		if off, ok := in["offset"].(float64); ok {
			in["offset"] = max(off-1, 0)
		}
	case "Grep":
		if g, ok := in["glob"].(string); ok && g != "" {
			in["include"] = g
		}
	case "TodoWrite":
		if list, ok := in["todos"].([]any); ok {
			for _, item := range list {
				if m, ok := item.(map[string]any); ok {
					if af, ok := m["activeForm"]; ok {
						m["active_form"] = af
						delete(m, "activeForm")
					}
				}
			}
		}
	case "WebFetch":
		if _, ok := in["format"]; !ok {
			in["format"] = "markdown"
		}
	}
	out, err := json.Marshal(in)
	if err != nil {
		return string(raw)
	}
	return string(out)
}

// ccTodos reads the todo list from a TodoWrite input.
func ccTodos(raw json.RawMessage) []session.Todo {
	var in struct {
		Todos []struct {
			Content    string `json:"content"`
			Status     string `json:"status"`
			ActiveForm string `json:"activeForm"`
		} `json:"todos"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return nil
	}
	todos := make([]session.Todo, 0, len(in.Todos))
	for _, t := range in.Todos {
		todos = append(todos, session.Todo{Content: t.Content, Status: session.TodoStatus(t.Status), ActiveForm: t.ActiveForm})
	}
	return todos
}

// ccTodosMetadata mirrors the todos tool's progress bookkeeping.
func ccTodosMetadata(old, todos []session.Todo) tools.TodosResponseMetadata {
	oldStatus := make(map[string]session.TodoStatus, len(old))
	for _, t := range old {
		oldStatus[t.Content] = t.Status
	}
	meta := tools.TodosResponseMetadata{IsNew: len(old) == 0, Todos: todos, Total: len(todos)}
	for _, t := range todos {
		prev, existed := oldStatus[t.Content]
		switch t.Status {
		case session.TodoStatusCompleted:
			meta.Completed++
			if existed && prev != session.TodoStatusCompleted {
				meta.JustCompleted = append(meta.JustCompleted, t.Content)
			}
		case session.TodoStatusInProgress:
			if !existed || prev != session.TodoStatusInProgress {
				meta.JustStarted = t.ActiveForm
				if meta.JustStarted == "" {
					meta.JustStarted = t.Content
				}
			}
		}
	}
	return meta
}

// ccImage returns the first base64 image in a tool_result content list.
func ccImage(raw json.RawMessage) (data, mime string) {
	var parts []struct {
		Type   string `json:"type"`
		Source struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
		} `json:"source"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return "", ""
	}
	for _, p := range parts {
		if p.Type == "image" && p.Source.Type == "base64" && p.Source.Data != "" {
			if _, err := base64.StdEncoding.DecodeString(p.Source.Data); err == nil {
				return p.Source.Data, p.Source.MediaType
			}
		}
	}
	return "", ""
}

// ccApplyEdit applies one string replacement the way Claude Code's Edit
// tool does.
func ccApplyEdit(content, oldString, newString string, replaceAll bool) string {
	if oldString == "" {
		return newString + content
	}
	if replaceAll {
		return strings.ReplaceAll(content, oldString, newString)
	}
	return strings.Replace(content, oldString, newString, 1)
}

// ccToolMetadata builds the Ta-Da metadata JSON for a finished Claude Code
// tool call from its input and the CLI's structured tool_use_result. It
// returns "" when there is nothing useful to add.
func ccToolMetadata(ccName string, input json.RawMessage, result json.RawMessage, text, workDir string) string {
	var meta any
	switch ccName {
	case "Bash":
		var in struct {
			Description string `json:"description"`
		}
		_ = json.Unmarshal(input, &in)
		out := text
		var r struct {
			Stdout string `json:"stdout"`
			Stderr string `json:"stderr"`
		}
		if json.Unmarshal(result, &r) == nil && (r.Stdout != "" || r.Stderr != "") {
			out = strings.TrimRight(r.Stdout, "\n")
			if r.Stderr != "" {
				if out != "" {
					out += "\n"
				}
				out += strings.TrimRight(r.Stderr, "\n")
			}
		}
		meta = tools.BashResponseMetadata{Output: out, Description: in.Description, WorkingDirectory: workDir}
	case "Read":
		var r struct {
			File struct {
				FilePath string `json:"filePath"`
				Content  string `json:"content"`
			} `json:"file"`
		}
		if json.Unmarshal(result, &r) != nil || r.File.Content == "" {
			return ""
		}
		meta = tools.ViewResponseMetadata{FilePath: r.File.FilePath, Content: r.File.Content}
	case "Edit":
		var r struct {
			FilePath     string `json:"filePath"`
			OldString    string `json:"oldString"`
			NewString    string `json:"newString"`
			OriginalFile string `json:"originalFile"`
			ReplaceAll   bool   `json:"replaceAll"`
		}
		if json.Unmarshal(result, &r) != nil || r.FilePath == "" {
			return ""
		}
		newContent := ccApplyEdit(r.OriginalFile, r.OldString, r.NewString, r.ReplaceAll)
		_, adds, rems := diff.GenerateDiff(r.OriginalFile, newContent, r.FilePath)
		meta = tools.EditResponseMetadata{Additions: adds, Removals: rems, OldContent: r.OriginalFile, NewContent: newContent}
	case "MultiEdit":
		var r struct {
			FilePath             string `json:"filePath"`
			OriginalFileContents string `json:"originalFileContents"`
			OriginalFile         string `json:"originalFile"`
		}
		if json.Unmarshal(result, &r) != nil || r.FilePath == "" {
			return ""
		}
		original := r.OriginalFileContents
		if original == "" {
			original = r.OriginalFile
		}
		var in tools.MultiEditParams
		_ = json.Unmarshal(input, &in)
		newContent := original
		for _, e := range in.Edits {
			newContent = ccApplyEdit(newContent, e.OldString, e.NewString, e.ReplaceAll)
		}
		_, adds, rems := diff.GenerateDiff(original, newContent, r.FilePath)
		meta = tools.MultiEditResponseMetadata{Additions: adds, Removals: rems, OldContent: original, NewContent: newContent, EditsApplied: len(in.Edits)}
	case "Write":
		var r struct {
			FilePath     string  `json:"filePath"`
			Content      string  `json:"content"`
			OriginalFile *string `json:"originalFile"`
		}
		if json.Unmarshal(result, &r) != nil || r.FilePath == "" {
			return ""
		}
		var original string
		if r.OriginalFile != nil {
			original = *r.OriginalFile
		}
		d, adds, rems := diff.GenerateDiff(original, r.Content, r.FilePath)
		meta = tools.WriteResponseMetadata{Diff: d, Additions: adds, Removals: rems}
	default:
		return ""
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return ""
	}
	return string(b)
}

// ccPermission turns a Claude Code permission request into the Ta-Da
// permission prompt for the matching tool.
func ccPermission(req claudecode.PermissionRequest, workDir string) (name, action, description, path string, params any) {
	name = ccToolName(req.ToolName)
	var in map[string]any
	_ = json.Unmarshal(req.Input, &in)
	str := func(k string) string {
		s, _ := in[k].(string)
		return s
	}
	path = workDir
	switch req.ToolName {
	case "Bash":
		cmd := str("command")
		return name, "execute", "Execute command: " + cmd, workDir, tools.BashPermissionsParams{
			Command:     cmd,
			Description: str("description"),
			WorkingDir:  workDir,
		}
	case "Edit", "Write", "MultiEdit":
		file := str("file_path")
		oldContent, newContent := ccProposedChange(req.ToolName, req.Input, file)
		description = "Edit file " + file
		if req.ToolName == "Write" {
			description = "Create file " + file
		}
		dir := fsext.PathOrPrefix(file, workDir)
		switch req.ToolName {
		case "Edit":
			params = tools.EditPermissionsParams{FilePath: file, OldContent: oldContent, NewContent: newContent}
		case "Write":
			params = tools.WritePermissionsParams{FilePath: file, OldContent: oldContent, NewContent: newContent}
		default:
			params = tools.MultiEditPermissionsParams{FilePath: file, OldContent: oldContent, NewContent: newContent}
		}
		return name, "write", description, dir, params
	case "Read":
		file := str("file_path")
		return name, "read", "Read file " + file, fsext.PathOrPrefix(file, workDir), tools.ViewPermissionsParams{FilePath: file}
	case "WebFetch":
		u := str("url")
		return name, "fetch", "Fetch content from URL: " + u, workDir, tools.FetchPermissionsParams{URL: u, Format: "markdown"}
	}
	if req.BlockedPath != "" {
		path = req.BlockedPath
	}
	pretty, _ := json.MarshalIndent(in, "", "  ")
	return name, "execute", "Claude Code wants to use " + req.ToolName, path, string(pretty)
}

// ccProposedChange computes the whole-file before/after for an edit the
// CLI is asking to make, so the permission prompt can show a diff.
func ccProposedChange(ccName string, input json.RawMessage, file string) (string, string) {
	before := readFileString(file)
	switch ccName {
	case "Write":
		var in tools.WriteParams
		_ = json.Unmarshal(input, &in)
		return before, in.Content
	case "Edit":
		var in tools.EditParams
		_ = json.Unmarshal(input, &in)
		return before, ccApplyEdit(before, in.OldString, in.NewString, in.ReplaceAll)
	default:
		var in tools.MultiEditParams
		_ = json.Unmarshal(input, &in)
		after := before
		for _, e := range in.Edits {
			after = ccApplyEdit(after, e.OldString, e.NewString, e.ReplaceAll)
		}
		return before, after
	}
}

// readFileString returns a file's contents, or "" when it can't be read
// (for example a file that doesn't exist yet).
func readFileString(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}
