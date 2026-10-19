package version

import "testing"

func TestDisplay(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"v0.1.0":                               "v0.1.0",
		"devel":                                "devel",
		"v0.1.1-0.20261019163516-ef7bc645ab12": "dev+ef7bc64",
		"v0.1.1-0.20261019163516-ef7bc645ab12+dirty": "dev+ef7bc64-dirty",
		"v0.2.0-rc.1.0.20261019163516-ef7bc645ab12":  "dev+ef7bc64",
	}
	for in, want := range cases {
		if got := display(in); got != want {
			t.Errorf("display(%q) = %q, want %q", in, got, want)
		}
	}
}
