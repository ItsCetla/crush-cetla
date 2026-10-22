package rewind

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"

	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/permission"
)

// bashToolName matches tools.BashToolName without importing the tools
// package.
const bashToolName = "bash"

// Timeline lists the session's turns, oldest first.
func (s *Service) Timeline(ctx context.Context, sessionID string) ([]Turn, error) {
	if err := s.messages.FlushAll(ctx); err != nil {
		return nil, fmt.Errorf("flushing messages: %w", err)
	}
	msgs, err := s.messages.List(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("listing messages: %w", err)
	}
	files, err := s.history.ListBySession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("listing file history: %w", err)
	}
	byCall := map[string][]permission.Action{}
	if s.actions != nil {
		for _, a := range s.actions(ctx) {
			if a.ToolCallID != "" {
				byCall[a.ToolCallID] = append(byCall[a.ToolCallID], a)
			}
		}
	}

	starts := turnStarts(msgs)
	turns := make([]Turn, 0, len(starts))
	for i, start := range starts {
		end := len(msgs)
		hi := int64(math.MaxInt64)
		if i+1 < len(starts) {
			end = starts[i+1]
			hi = msgs[end].CreatedAt
		}
		first := msgs[start]
		t := Turn{
			Index:     i + 1,
			MessageID: first.ID,
			Prompt:    promptOf(first),
			CreatedAt: first.CreatedAt,
			Files:     changedBetween(files, first.CreatedAt, hi),
		}
		for _, m := range msgs[start:end] {
			if m.HasShellCommand() {
				t.Bash = true
			}
			for _, tc := range m.ToolCalls() {
				if tc.Name == bashToolName {
					t.Bash = true
				}
				for _, a := range byCall[tc.ID] {
					if a.Class == "" {
						continue
					}
					if t.Actions == nil {
						t.Actions = map[permission.Class]int{}
					}
					t.Actions[a.Class]++
				}
			}
		}
		turns = append(turns, t)
	}
	return turns, nil
}

// turnStarts returns the indexes of the messages that start turns.
func turnStarts(msgs []message.Message) []int {
	var out []int
	for i, m := range msgs {
		if isTurnStart(m) {
			out = append(out, i)
		}
	}
	return out
}

// changedBetween returns the sorted paths with a version recorded in
// [lo, hi).
func changedBetween(files []history.File, lo, hi int64) []string {
	var out []string
	for _, f := range files {
		if f.CreatedAt >= lo && f.CreatedAt < hi {
			out = append(out, f.Path)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// byPath groups a session's versions by path, each group oldest first.
func byPath(files []history.File) map[string][]history.File {
	out := map[string][]history.File{}
	for _, f := range files {
		out[f.Path] = append(out[f.Path], f)
	}
	for _, rs := range out {
		slices.SortStableFunc(rs, func(a, b history.File) int {
			return cmp.Or(
				cmp.Compare(a.CreatedAt, b.CreatedAt),
				cmp.Compare(a.Version, b.Version),
				cmp.Compare(a.ID, b.ID),
			)
		})
	}
	return out
}
