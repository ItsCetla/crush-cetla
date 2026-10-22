package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCleanCommitMessage(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"feat: add review screen":                                    "feat: add review screen",
		"<think>hmm</think>\nfix: keep cursor":                       "fix: keep cursor",
		"```\nfeat(ui): add thing\n\nBody line.  \n```":              "feat(ui): add thing\n\nBody line.",
		"```text\nchore: bump\n```":                                  "chore: bump",
		"Commit message: docs: explain review":                       "docs: explain review",
		"\"refactor: split dialog\"":                                 "refactor: split dialog",
		"  \n\nfix: trim\n\n  ":                                      "fix: trim",
		"fix: thing\n\nWhy it changed.\nAnd more detail on the fix.": "fix: thing\n\nWhy it changed.\nAnd more detail on the fix.",
	}
	for in, want := range cases {
		require.Equal(t, want, cleanCommitMessage(in), "input %q", in)
	}
}

func TestCommitMessageUserPrompt(t *testing.T) {
	t.Parallel()
	got := commitMessageUserPrompt("+added\n", "abc123 feat: earlier\n")
	require.Contains(t, got, "abc123 feat: earlier")
	require.Contains(t, got, "+added")

	got = commitMessageUserPrompt(strings.Repeat("x\n", maxCommitDiff), "")
	require.NotContains(t, got, "Recent commits")
	require.Contains(t, got, "[diff truncated")
	require.Less(t, len(got), maxCommitDiff+200)
}
