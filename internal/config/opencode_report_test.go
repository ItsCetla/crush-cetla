package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPackageFetch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command []string
		flagged string // substring the note must contain, or "" for none
	}{
		{"npx latest", []string{"npx", "-y", "@playwright/mcp@latest"}, "npx contacts"},
		{"npx pinned", []string{"npx", "-y", "@playwright/mcp@0.0.83"}, "npx contacts"},
		{"npx path in one string", []string{"/opt/homebrew/bin/npx -y pkg@1.0.0"}, "npx contacts"},
		{"npx prefer offline", []string{"npx", "--prefer-offline", "-y", "@playwright/mcp@latest"}, ""},
		{"npx offline", []string{"npx", "--offline", "-y", "pkg@1.0.0"}, ""},
		{"uvx latest", []string{"uvx", "blender-mcp@latest"}, "blender-mcp@latest asks"},
		{"uvx pinned", []string{"uvx", "blender-mcp==1.2.0"}, ""},
		{"bunx", []string{"bunx", "pkg"}, "bunx can download"},
		{"pnpm dlx", []string{"pnpm", "dlx", "pkg"}, "pnpm can download"},
		{"yarn dlx", []string{"yarn", "dlx", "pkg"}, "yarn can download"},
		{"pnpm exec", []string{"pnpm", "exec", "pkg"}, ""},
		{"plain binary", []string{"/usr/local/bin/bench-mcp", "--stdio"}, ""},
		{"empty", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := packageFetch(tt.command)
			if tt.flagged == "" {
				require.Empty(t, got)
				return
			}
			require.Contains(t, got, tt.flagged)
		})
	}
}
