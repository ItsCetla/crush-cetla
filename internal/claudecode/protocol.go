// Package claudecode runs the user's own Claude Code CLI ("claude") as a
// turn engine and speaks its stream-json protocol over stdin/stdout.
//
// Ta-Da does not talk to Anthropic directly on this path: the CLI handles
// auth (the user's own login), the model loop, tools and compaction. Ta-Da
// sends the prompt, answers permission requests, and renders the events.
package claudecode

import (
	"encoding/json"
	"strings"
)

// Event is one line of `--output-format stream-json` output. Only the
// fields Ta-Da uses are decoded; Raw always holds the full line.
type Event struct {
	Type            string  `json:"type"`
	Subtype         string  `json:"subtype,omitempty"`
	SessionID       string  `json:"session_id,omitempty"`
	ParentToolUseID *string `json:"parent_tool_use_id,omitempty"`
	UUID            string  `json:"uuid,omitempty"`

	// user: IsReplay marks the CLI's echo of a user message it added to
	// the conversation (--replay-user-messages); IsSynthetic marks one
	// the CLI made up itself.
	IsReplay    bool `json:"isReplay,omitempty"`
	IsSynthetic bool `json:"isSynthetic,omitempty"`

	// stream_event
	Event *StreamEvent `json:"event,omitempty"`

	// assistant / user
	Message       *Message        `json:"message,omitempty"`
	ToolUseResult json.RawMessage `json:"tool_use_result,omitempty"`

	// result
	IsError      bool     `json:"is_error,omitempty"`
	Result       string   `json:"result,omitempty"`
	TotalCostUSD float64  `json:"total_cost_usd,omitempty"`
	Usage        *Usage   `json:"usage,omitempty"`
	NumTurns     int      `json:"num_turns,omitempty"`
	Errors       []string `json:"errors,omitempty"`

	// system/init
	Model string `json:"model,omitempty"`

	// control_request / control_cancel_request / control_response
	RequestID string          `json:"request_id,omitempty"`
	Request   json.RawMessage `json:"request,omitempty"`
	Response  json.RawMessage `json:"response,omitempty"`

	Raw json.RawMessage `json:"-"`
}

// IsSubagent reports whether the event belongs to a nested Task agent
// rather than the main conversation.
func (e Event) IsSubagent() bool {
	return e.ParentToolUseID != nil && *e.ParentToolUseID != ""
}

// StreamEvent is a raw Anthropic streaming event forwarded by
// --include-partial-messages.
type StreamEvent struct {
	Type         string        `json:"type"`
	Index        int           `json:"index"`
	Message      *Message      `json:"message,omitempty"`
	ContentBlock *ContentBlock `json:"content_block,omitempty"`
	Delta        *Delta        `json:"delta,omitempty"`
	Usage        *Usage        `json:"usage,omitempty"`
}

// Delta is the payload of content_block_delta and message_delta.
type Delta struct {
	Type        string `json:"type,omitempty"`
	Text        string `json:"text,omitempty"`
	Thinking    string `json:"thinking,omitempty"`
	Signature   string `json:"signature,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	StopReason  string `json:"stop_reason,omitempty"`
}

// ContentBlock is one block of an assistant or user message.
type ContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// ResultText flattens a tool_result block's content, which is either a
// string or a list of {type:"text"} / {type:"image"} blocks.
func (b ContentBlock) ResultText() string {
	return flattenContent(b.Content)
}

// Message is an Anthropic message as embedded in assistant/user events.
type Message struct {
	ID         string          `json:"id,omitempty"`
	Role       string          `json:"role,omitempty"`
	Model      string          `json:"model,omitempty"`
	Content    json.RawMessage `json:"content,omitempty"`
	StopReason string          `json:"stop_reason,omitempty"`
	Usage      *Usage          `json:"usage,omitempty"`
}

// Blocks decodes the message content. A plain string becomes one text block.
func (m *Message) Blocks() []ContentBlock {
	if m == nil || len(m.Content) == 0 {
		return nil
	}
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return []ContentBlock{{Type: "text", Text: s}}
	}
	var blocks []ContentBlock
	_ = json.Unmarshal(m.Content, &blocks)
	return blocks
}

// Usage mirrors the Anthropic usage object.
type Usage struct {
	InputTokens              int64 `json:"input_tokens,omitempty"`
	OutputTokens             int64 `json:"output_tokens,omitempty"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens,omitempty"`
}

// PermissionRequest is the body of a can_use_tool control request.
type PermissionRequest struct {
	Subtype        string          `json:"subtype"`
	ToolName       string          `json:"tool_name"`
	Input          json.RawMessage `json:"input"`
	ToolUseID      string          `json:"tool_use_id,omitempty"`
	BlockedPath    string          `json:"blocked_path,omitempty"`
	DecisionReason json.RawMessage `json:"decision_reason,omitempty"`
}

// Decision answers a PermissionRequest.
type Decision struct {
	Allow bool
	// UpdatedInput replaces the tool input when allowing; nil keeps the
	// original input.
	UpdatedInput json.RawMessage
	// Message is shown to the model when denying.
	Message string
	// Interrupt stops the whole turn after a deny.
	Interrupt bool
}

func flattenContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return string(raw)
	}
	var sb strings.Builder
	for _, p := range parts {
		switch p.Type {
		case "text":
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(p.Text)
		case "image":
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString("[image]")
		}
	}
	return sb.String()
}
