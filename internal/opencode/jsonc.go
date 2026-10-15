package opencode

import (
	"bytes"
	"errors"
	"fmt"
)

// errUnterminatedComment is returned for a /* comment that never closes.
var errUnterminatedComment = errors.New("unterminated block comment")

// jsoncToJSON rewrites JSONC (JSON with // and /* */ comments and trailing
// commas) into strict JSON. String contents are copied verbatim, so comment
// markers and commas inside strings are preserved. Comments are replaced by
// whitespace (newlines are kept) so byte offsets in later JSON errors still
// point at the right line. Anything else that is not valid JSON is left for
// the JSON decoder to reject.
func jsoncToJSON(src []byte) ([]byte, error) {
	src = bytes.TrimPrefix(src, []byte("\xef\xbb\xbf"))
	out := make([]byte, 0, len(src))
	// pendingComma holds the index in out of a comma that may turn out to
	// be trailing, or -1.
	pendingComma := -1
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case c == '"':
			pendingComma = -1
			j := i + 1
			for j < len(src) {
				if src[j] == '\\' {
					j += 2
					continue
				}
				if src[j] == '"' {
					break
				}
				j++
			}
			if j >= len(src) {
				// Unterminated string: copy the rest and let the decoder
				// report it.
				out = append(out, src[i:]...)
				return out, nil
			}
			out = append(out, src[i:j+1]...)
			i = j
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			j := i
			for j < len(src) && src[j] != '\n' {
				out = append(out, ' ')
				j++
			}
			i = j - 1
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end := bytes.Index(src[i+2:], []byte("*/"))
			if end < 0 {
				return nil, fmt.Errorf("%w at offset %d", errUnterminatedComment, i)
			}
			stop := i + 2 + end + 2
			for _, b := range src[i:stop] {
				if b == '\n' {
					out = append(out, '\n')
				} else {
					out = append(out, ' ')
				}
			}
			i = stop - 1
		case c == ',':
			pendingComma = len(out)
			out = append(out, c)
		case c == '}' || c == ']':
			if pendingComma >= 0 {
				out[pendingComma] = ' '
				pendingComma = -1
			}
			out = append(out, c)
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			out = append(out, c)
		default:
			pendingComma = -1
			out = append(out, c)
		}
	}
	return out, nil
}
