package agent

import (
	"context"
	"errors"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/claudecode"
)

// errClaudeCodeDirect is returned when something calls a Claude Code
// model directly. Claude Code only runs whole agent turns (see
// runClaudeCode), so one-off calls like titles and summaries skip it.
var errClaudeCodeDirect = errors.New("claude code models only run agent turns")

// claudeCodeProvider stands in for a fantasy provider so Claude Code
// models fit Ta-Da's model plumbing.
type claudeCodeProvider struct{}

func (claudeCodeProvider) Name() string { return claudecode.ProviderType }

func (claudeCodeProvider) LanguageModel(_ context.Context, modelID string) (fantasy.LanguageModel, error) {
	return claudeCodeModel{id: modelID}, nil
}

type claudeCodeModel struct{ id string }

func (m claudeCodeModel) Provider() string { return claudecode.ProviderType }
func (m claudeCodeModel) Model() string    { return m.id }

func (claudeCodeModel) Generate(context.Context, fantasy.Call) (*fantasy.Response, error) {
	return nil, errClaudeCodeDirect
}

func (claudeCodeModel) Stream(context.Context, fantasy.Call) (fantasy.StreamResponse, error) {
	return nil, errClaudeCodeDirect
}

func (claudeCodeModel) GenerateObject(context.Context, fantasy.ObjectCall) (*fantasy.ObjectResponse, error) {
	return nil, errClaudeCodeDirect
}

func (claudeCodeModel) StreamObject(context.Context, fantasy.ObjectCall) (fantasy.ObjectStreamResponse, error) {
	return nil, errClaudeCodeDirect
}
