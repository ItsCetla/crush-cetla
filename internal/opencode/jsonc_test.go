package opencode

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestJSONCToJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want any
	}{
		{
			name: "line comments",
			in:   "{\n  // comment\n  \"a\": 1 // trailing\n}",
			want: map[string]any{"a": 1.0},
		},
		{
			name: "block comment across lines",
			in:   "{\n  /* one\n     two */ \"a\": /* inline */ 2\n}",
			want: map[string]any{"a": 2.0},
		},
		{
			name: "comment markers inside strings",
			in:   `{"url": "https://example.com//path", "b": "/* not a comment */", "c": "// nope"}`,
			want: map[string]any{
				"url": "https://example.com//path",
				"b":   "/* not a comment */",
				"c":   "// nope",
			},
		},
		{
			name: "escaped quotes do not end strings",
			in:   `{"a": "say \"hi\" // still string", "b": "back\\"}`,
			want: map[string]any{"a": `say "hi" // still string`, "b": `back\`},
		},
		{
			name: "trailing commas",
			in:   "{\"a\": [1, 2, 3,], \"b\": {\"c\": true,},}",
			want: map[string]any{"a": []any{1.0, 2.0, 3.0}, "b": map[string]any{"c": true}},
		},
		{
			name: "trailing comma before comment",
			in:   "{\"a\": 1, // last\n}",
			want: map[string]any{"a": 1.0},
		},
		{
			name: "comma inside string is kept",
			in:   `{"a": ",}", "b": [","]}`,
			want: map[string]any{"a": ",}", "b": []any{","}},
		},
		{
			name: "byte order mark",
			in:   "\xef\xbb\xbf{\"a\": 1}",
			want: map[string]any{"a": 1.0},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out, err := jsoncToJSON([]byte(tt.in))
			require.NoError(t, err)
			var got any
			require.NoError(t, json.Unmarshal(out, &got), string(out))
			require.Equal(t, tt.want, got)
		})
	}
}

func TestJSONCToJSONErrors(t *testing.T) {
	t.Parallel()

	_, err := jsoncToJSON([]byte("{\"a\": 1 /* never closed"))
	require.ErrorIs(t, err, errUnterminatedComment)

	for _, in := range []string{
		`{"a": 1,,}`,
		`{"a": }`,
		`{"a": "unterminated}`,
		`[1, 2]`,
		`"just a string"`,
		``,
		`null`,
	} {
		_, err := parseDocument([]byte(in))
		require.Error(t, err, in)
	}
}
