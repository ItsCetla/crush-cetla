package export

import (
	"regexp"
	"strings"
)

// Redacted is what a scrubbed secret is replaced with.
const Redacted = "[REDACTED]"

var (
	// privateKeyRE matches a whole PEM private key block.
	privateKeyRE = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)

	// tokenREs match credentials that announce themselves with a prefix.
	tokenREs = []*regexp.Regexp{
		// OpenAI, Anthropic and most other "sk-" style keys.
		regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`),
		// GitHub personal, OAuth, user, server and refresh tokens.
		regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`),
		regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`),
		// AWS access key IDs.
		regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`),
		// Slack tokens.
		regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`),
		// Google API keys.
		regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35,}`),
		// JSON web tokens.
		regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`),
	}

	// bearerRE matches an HTTP bearer credential.
	bearerRE = regexp.MustCompile(`(?i)\b(bearer)\s+[A-Za-z0-9._~+/=-]{8,}`)

	// envSecretRE matches an assignment to an environment-style name
	// that holds a secret, such as OPENAI_API_KEY=... or
	// "GITHUB_TOKEN": "...". The secret word must be a whole
	// underscore-separated part of the name, so KEYBOARD=... is left be.
	envSecretRE = regexp.MustCompile(`\b((?:[A-Z0-9]+_)*(?:(?:API)?KEY|TOKEN|SECRET|PASSWORD|PASSWD)S?(?:_[A-Z0-9]+)*)(["']?\s*(?::=|[:=])\s*["']?)([^\s"'` + "`" + `,;&|)]+)`)

	// fieldSecretRE matches a quoted value given to a lower-case field
	// that holds a secret, such as api_key: "..." or password := "...".
	fieldSecretRE = regexp.MustCompile(`(?i)\b((?:api[_-]?key|access[_-]?token|auth[_-]?token|refresh[_-]?token|client[_-]?secret|secret[_-]?key|password|passwd)["']?\s*(?::=|[:=])\s*)(["'])([^"'\s]{8,})(["'])`)

	numberRE = regexp.MustCompile(`^[0-9.]+$`)
)

// RedactSecrets replaces API keys, tokens and other credentials in s
// with [Redacted]. It leaves short and numeric values alone so settings
// such as MAX_TOKENS=4096 survive.
func RedactSecrets(s string) string {
	if s == "" {
		return s
	}
	s = privateKeyRE.ReplaceAllString(s, "[REDACTED PRIVATE KEY]")
	for _, re := range tokenREs {
		s = re.ReplaceAllString(s, Redacted)
	}
	s = bearerRE.ReplaceAllString(s, "${1} "+Redacted)
	s = envSecretRE.ReplaceAllStringFunc(s, func(m string) string {
		parts := envSecretRE.FindStringSubmatch(m)
		if !looksSecret(parts[3]) {
			return m
		}
		return parts[1] + parts[2] + Redacted
	})
	s = fieldSecretRE.ReplaceAllStringFunc(s, func(m string) string {
		parts := fieldSecretRE.FindStringSubmatch(m)
		if parts[3] == Redacted {
			return m
		}
		return parts[1] + parts[2] + Redacted + parts[4]
	})
	return s
}

// looksSecret reports whether an assigned value is worth scrubbing. It
// skips variable references, placeholders, numbers and values too short
// to be a credential.
func looksSecret(v string) bool {
	switch {
	case v == Redacted,
		strings.HasPrefix(v, "$"),
		strings.HasPrefix(v, "%"),
		strings.HasPrefix(v, "<"),
		strings.HasPrefix(v, "{{"),
		len(v) < 6,
		numberRE.MatchString(v):
		return false
	}
	return true
}

// homeReplacer returns a function that swaps the absolute home directory
// for "~" wherever it begins a path. It returns nil when home is empty or
// the filesystem root.
func homeReplacer(home string) func(string) string {
	home = strings.TrimRight(home, `/\`)
	if home == "" {
		return nil
	}
	re := regexp.MustCompile(regexp.QuoteMeta(home) + `([/\\]|$|[^A-Za-z0-9_.-])`)
	return func(s string) string {
		if !strings.Contains(s, home) {
			return s
		}
		return re.ReplaceAllString(s, "~${1}")
	}
}
