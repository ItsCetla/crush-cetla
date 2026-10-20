package artifacts

import (
	"encoding/json"

	"github.com/google/jsonschema-go/jsonschema"
)

// ToolSchema returns the JSON schema properties and required fields of a
// tool's input type, built the same way the MCP server builds them. Ta-Da's
// own tool wrapper can't express free-form maps, so the native tools use
// this instead.
func ToolSchema[T any]() (map[string]any, []string) {
	s, err := jsonschema.For[T](nil)
	if err != nil {
		return map[string]any{}, nil
	}
	b, err := json.Marshal(s)
	if err != nil {
		return map[string]any{}, nil
	}
	var m struct {
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}
	_ = json.Unmarshal(b, &m)
	if m.Properties == nil {
		m.Properties = map[string]any{}
	}
	return m.Properties, m.Required
}
