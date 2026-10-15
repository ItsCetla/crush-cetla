package opencode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func mapEnv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func TestSubstituteEnv(t *testing.T) {
	t.Parallel()

	s := substituter{getenv: mapEnv(map[string]string{
		"KEY":     `sk-"quoted"`,
		"TIMEOUT": "1500",
		"BRACE":   "{file:/nope}",
	})}

	got, warnings := s.substitute(`{"a": "{env:KEY}", "t": {env:TIMEOUT}, "u": "{env:UNSET}"}`, "test", "/")
	require.Empty(t, warnings)
	// Values are inserted raw, not JSON-escaped, exactly like OpenCode.
	require.Equal(t, `{"a": "sk-"quoted"", "t": 1500, "u": ""}`, got)

	// {env:...} runs before {file:...}, so a variable can expand to a file
	// reference.
	got, warnings = s.substitute(`{"a": "{env:BRACE}"}`, "test", "/")
	require.Equal(t, `{"a": ""}`, got)
	require.Len(t, warnings, 1)
}

func TestSubstituteFile(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "secrets", "key.txt"), "  line \"one\"\n\tline\\two\n\n")
	writeFile(t, filepath.Join(home, "token"), "tok-123\n")
	abs := filepath.Join(t.TempDir(), "abs.txt")
	writeFile(t, abs, "absolute")

	s := substituter{getenv: mapEnv(nil), home: home}

	tests := []struct {
		name         string
		in           string
		want         string
		wantWarnings int
	}{
		{
			name: "relative to config dir and JSON escaped",
			in:   `{"k": "{file:secrets/key.txt}"}`,
			want: `{"k": "line \"one\"\n\tline\\two"}`,
		},
		{
			name: "home expansion",
			in:   `{"k": "{file:~/token}"}`,
			want: `{"k": "tok-123"}`,
		},
		{
			name: "absolute path",
			in:   `{"k": "{file:` + filepath.ToSlash(abs) + `}"}`,
			want: `{"k": "absolute"}`,
		},
		{
			name:         "missing file substitutes empty with a warning",
			in:           `{"k": "{file:missing.txt}"}`,
			want:         `{"k": ""}`,
			wantWarnings: 1,
		},
		{
			name: "commented-out line is left alone",
			in:   "{\n  // \"k\": \"{file:missing.txt}\",\n  \"j\": 1\n}",
			want: "{\n  // \"k\": \"{file:missing.txt}\",\n  \"j\": 1\n}",
		},
		{
			name:         "only the line prefix counts as a comment",
			in:           `{"k": "x"} // {file:secrets/key.txt}` + "\n" + `"{file:missing.txt}"`,
			want:         `{"k": "x"} // line \"one\"\n\tline\\two` + "\n" + `""`,
			wantWarnings: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, warnings := s.substitute(tt.in, "cfg", dir)
			require.Equal(t, tt.want, got)
			require.Len(t, warnings, tt.wantWarnings)
		})
	}
}

func TestSubstituteFileParsesAsJSON(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "prompt.md"), "He said \"hi\"\nand left \\o/")
	writeFile(t, filepath.Join(dir, "opencode.json"), `{"provider": {"p": {"options": {"apiKey": "{file:./prompt.md}"}}}}`)

	cfg, err := LoadConfig(LoadOptions{Paths: Paths{ConfigDir: dir}})
	require.NoError(t, err)
	require.Empty(t, cfg.Warnings)
	require.Equal(t, "He said \"hi\"\nand left \\o/", cfg.Providers["p"].APIKey)
}
