//go:build windows

package styles

// CodespanPadding is the padding rendered around inline code spans in
// markdown; selection copies turn it back into backticks (see
// list.HighlightContent).
//
// On Windows it is a plain no-break space. The console host (conhost, which
// also sits under Windows Terminal as ConPTY) does not join the U+FE0E
// variation selector used elsewhere into the preceding cell: it draws the
// selector as its own "□" cell. Each codespan then takes two more cells
// than the renderer budgeted, every line holding inline code shifts right,
// and the frame differ leaves stale text at the right edge.
//
// The cost is that a real no-break space in message text also copies as a
// backtick on Windows.
const CodespanPadding string = " "
