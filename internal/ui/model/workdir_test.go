package model

import "testing"

func TestParseCDCommand(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		bang bool
		arg  string
		ok   bool
	}{
		{"/cd", false, "", true},
		{"/cd ~/code/app", false, "~/code/app", true},
		{"  /CD   ../other  ", false, "../other", true},
		{"/directory -", false, "-", true},
		{"/dir /tmp", false, "/tmp", true},
		{"/cd my folder", false, "my folder", true},
		{"/cdx", false, "", false},
		{"/compact", false, "", false},
		{"cd /tmp", false, "", false},
		{"/cd /tmp", true, "", false},
		{"/cd /tmp\nand more", false, "", false},
	}
	for _, c := range cases {
		arg, ok := parseCDCommand(c.in, c.bang)
		if ok != c.ok || arg != c.arg {
			t.Errorf("parseCDCommand(%q, %v) = %q, %v; want %q, %v", c.in, c.bang, arg, ok, c.arg, c.ok)
		}
	}
}
