package dialog

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrintable(t *testing.T) {
	t.Parallel()

	require.Equal(t, "?]0;pwned?a.go", printable("\x1b]0;pwned\x07a.go", false))
	require.Equal(t, "a?b", printable("a\nb", false))
	require.Equal(t, "a\n\tb", printable("a\r\n\tb", true))
	require.Equal(t, "x?y", printable("x\u009by", true))
	require.Equal(t, "héllo 世界", printable("héllo 世界", false))
	require.Equal(t, "fatal: bad ?[31m", safeLine("fatal:\n  bad \x1b[31m"))
}
