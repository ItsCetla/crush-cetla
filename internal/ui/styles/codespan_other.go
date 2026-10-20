//go:build !windows

package styles

// CodespanPadding is the padding rendered around inline code spans in
// markdown. It displays identically to the blank padding it replaced, but
// selection copies recognize it and turn it back into the original
// backticks (see list.HighlightContent).
//
// It is a no-break space tagged with the text-presentation variation
// selector (U+FE0E): the pair is a single one-cell grapheme that renders as
// a blank and, being non-breaking, keeps word wrap from tearing a codespan
// between its padding and its text. The selector makes the sentinel
// distinct from a real no-break space in message text (pasted text, some
// LLM output), which a selection copy must preserve verbatim.
//
// The selector must be U+FE0E, not the emoji-presentation U+FE0F:
// ansi.StringWidth measures any cluster ending in U+FE0F as two cells wide,
// while terminals render it as one, and that mismatch makes the frame
// differ repaint the line on every frame (visible as flicker).
//
// Windows uses a plain no-break space instead (codespan_windows.go).
const CodespanPadding string = " ︎"
