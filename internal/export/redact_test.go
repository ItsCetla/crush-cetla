package export

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedactSecrets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"openai key", "key: sk-proj-AbCdEfGhIjKlMnOpQrStUv", "key: [REDACTED]"},
		{"anthropic key", "sk-ant-api03-abcdefghijklmnop_QRSTUV-wxyz", "[REDACTED]"},
		{"github token", "token ghp_abcdefghijklmnopqrstuvwxyz0123456789", "token [REDACTED]"},
		{"github fine grained", "github_pat_11ABCDEFG0123456789_abcdefghijklmnop", "[REDACTED]"},
		{"aws key id", "AWS id AKIAIOSFODNN7EXAMPLE here", "AWS id [REDACTED] here"},
		{"slack token", "xoxb-1234567890-abcdefghij", "[REDACTED]"},
		{"google key", "AIzaSyA-abcdefghijklmnopqrstuvwxyz012345", "[REDACTED]"},
		{"bearer header", "Authorization: Bearer abc.def-ghi_jkl", "Authorization: Bearer [REDACTED]"},
		{"bearer lower case", "authorization: bearer 0123456789abcdef", "authorization: bearer [REDACTED]"},
		{"env assignment", "export STRIPE_SECRET_KEY=rk_live_51abcdef", "export STRIPE_SECRET_KEY=[REDACTED]"},
		{"env token", "GITHUB_TOKEN=abcdef123456 gh pr list", "GITHUB_TOKEN=[REDACTED] gh pr list"},
		{"env quoted", `DB_PASSWORD="hunter2hunter2"`, `DB_PASSWORD="[REDACTED]"`},
		{"json env", `{"OPENAI_API_KEY": "abc123def456"}`, `{"OPENAI_API_KEY": "[REDACTED]"}`},
		{"field secret", `client_secret: "s3cr3t-value-here"`, `client_secret: "[REDACTED]"`},
		{"go field", `apiKey := "abcdefgh12345678"`, `apiKey := "[REDACTED]"`},
		{"private key", "-----BEGIN RSA PRIVATE KEY-----\nMIIE\n-----END RSA PRIVATE KEY-----", "[REDACTED PRIVATE KEY]"},
		{"numeric setting kept", "MAX_TOKENS=4096", "MAX_TOKENS=4096"},
		{"variable reference kept", `ANTHROPIC_API_KEY=$ANTHROPIC_API_KEY`, `ANTHROPIC_API_KEY=$ANTHROPIC_API_KEY`},
		{"short value kept", "API_KEY=xyz", "API_KEY=xyz"},
		{"keyboard kept", "KEYBOARD_LAYOUT=colemak", "KEYBOARD_LAYOUT=colemak"},
		{"getenv kept", `os.Getenv("OPENAI_API_KEY")`, `os.Getenv("OPENAI_API_KEY")`},
		{"prose kept", "Ask the user for a token before continuing.", "Ask the user for a token before continuing."},
		{"risk kept", "risk-assessment-framework-document", "risk-assessment-framework-document"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := RedactSecrets(tt.in)
			require.Equal(t, tt.want, got)
			require.Equal(t, got, RedactSecrets(got), "redaction should be idempotent")
		})
	}
}

func TestHomeReplacer(t *testing.T) {
	t.Parallel()

	require.Nil(t, homeReplacer(""))
	require.Nil(t, homeReplacer("/"))

	r := homeReplacer("/Users/alice/")
	require.Equal(t, "~/src/app/main.go", r("/Users/alice/src/app/main.go"))
	require.Equal(t, "cd ~ && ls", r("cd /Users/alice && ls"))
	require.Equal(t, "~", r("/Users/alice"))
	require.Equal(t, `"~"`, r(`"/Users/alice"`))
	require.Equal(t, "/Users/alicebob/x", r("/Users/alicebob/x"))
	require.Equal(t, "/Users/alice.old/x", r("/Users/alice.old/x"))
}
