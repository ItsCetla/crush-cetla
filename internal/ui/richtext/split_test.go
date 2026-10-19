package richtext

import (
	"strings"
	"testing"
)

type seg struct {
	kind Kind
	text string
}

func TestSplit(t *testing.T) {
	t.Parallel()
	table := "| a | b |\n|---|---|\n| 1 | 2 |\n"
	chart := "```chart\n{\"type\":\"bar\"}\n```\n"
	tests := []struct {
		name string
		in   string
		want []seg
	}{
		{"empty", "", nil},
		{"prose only", "just *text*\n", []seg{{Prose, "just *text*\n"}}},
		{
			"mixed",
			"intro\n\n" + table + "\nmiddle\n\n" + chart + "\noutro",
			[]seg{
				{Prose, "intro\n\n"},
				{Table, table},
				{Prose, "\nmiddle\n\n"},
				{Chart, chart},
				{Prose, "\noutro"},
			},
		},
		{"table at end without newline", "x\n\n| a |\n|---|\n| 1 |", []seg{{Prose, "x\n\n"}, {Table, "| a |\n|---|\n| 1 |"}}},
		{"chart only", chart, []seg{{Chart, chart}}},
		{"tilde chart fence", "~~~chart\n{}\n~~~~\n", []seg{{Chart, "~~~chart\n{}\n~~~~\n"}}},
		{"go fence with pipes", "```go\n| a | b |\n|---|---|\n```\n", []seg{{Prose, "```go\n| a | b |\n|---|---|\n```\n"}}},
		{"chart with extra info", "```chart json\n{}\n```\n", []seg{{Prose, "```chart json\n{}\n```\n"}}},
		{"unclosed chart", "a\n\n```chart\n{\"type\":\"bar\"}\n", []seg{{Prose, "a\n\n```chart\n{\"type\":\"bar\"}\n"}}},
		{"short closing fence", "````chart\n{}\n```\n", []seg{{Prose, "````chart\n{}\n```\n"}}},
		{"table in list", "- item\n\n  | a |\n  |---|\n  | 1 |\n", []seg{{Prose, "- item\n\n  | a |\n  |---|\n  | 1 |\n"}}},
		{"table in blockquote", "> | a |\n> |---|\n> | 1 |\n", []seg{{Prose, "> | a |\n> |---|\n> | 1 |\n"}}},
		{"chart in blockquote", "> ```chart\n> {}\n> ```\n", []seg{{Prose, "> ```chart\n> {}\n> ```\n"}}},
		{
			"crlf",
			"hi\r\n\r\n| a |\r\n|---|\r\n| 1 |\r\n\r\n```chart\r\n{}\r\n```\r\nbye\r\n",
			[]seg{
				{Prose, "hi\r\n\r\n"},
				{Table, "| a |\r\n|---|\r\n| 1 |\r\n"},
				{Prose, "\r\n"},
				{Chart, "```chart\r\n{}\r\n```\r\n"},
				{Prose, "bye\r\n"},
			},
		},
		{"adjacent blocks", table + chart, []seg{{Table, table}, {Chart, chart}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Split(tt.in)
			assertLossless(t, tt.in, got)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d segments %q, want %d", len(got), got, len(tt.want))
			}
			for i, w := range tt.want {
				if got[i].Kind != w.kind || got[i].Text != w.text {
					t.Errorf("segment %d = %v %q, want %v %q", i, got[i].Kind, got[i].Text, w.kind, w.text)
				}
			}
		})
	}
}

func assertLossless(t *testing.T, in string, segs []Segment) {
	t.Helper()
	var b strings.Builder
	for i, s := range segs {
		if s.Text == "" {
			t.Errorf("segment %d is empty", i)
		}
		if i > 0 && s.Kind == Prose && segs[i-1].Kind == Prose {
			t.Errorf("segments %d and %d are both prose", i-1, i)
		}
		b.WriteString(s.Text)
	}
	if b.String() != in {
		t.Errorf("concatenation %q != input %q", b.String(), in)
	}
}

func TestSplitLosslessFuzzish(t *testing.T) {
	t.Parallel()
	parts := []string{
		"text\n", "\n", "| a | b |\n|--|--|\n| 1 | 2 |\n", "```chart\n{}\n```\n",
		"```\ncode | x\n```\n", "- list\n", "> quote\n", "```chart\n", "\r\n", "  indented\n",
	}
	for i := range parts {
		for j := range parts {
			for k := range parts {
				in := parts[i] + parts[j] + parts[k]
				assertLossless(t, in, Split(in))
			}
		}
	}
}

func TestChartBody(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{"```chart\n{\"a\":1}\n```\n", "{\"a\":1}"},
		{"```chart\nline1\nline2\n```", "line1\nline2"},
		{"```chart\n```\n", ""},
		{"```chart\r\n{}\r\n```\r\n", "{}"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			segs := Split(tt.in)
			if len(segs) != 1 || segs[0].Kind != Chart {
				t.Fatalf("not a single chart: %q", segs)
			}
			if got := segs[0].ChartBody(); got != tt.want {
				t.Errorf("ChartBody() = %q, want %q", got, tt.want)
			}
		})
	}
}
