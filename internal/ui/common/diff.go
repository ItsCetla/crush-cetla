package common

import (
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/ui/diffview"
	"github.com/charmbracelet/crush/internal/ui/styles"
)

// DiffFormatter returns a diff formatter with the given styles that can be
// used to format diff outputs.
func DiffFormatter(s *styles.Styles) *diffview.DiffView {
	formatDiff := diffview.New()
	diff := formatDiff.ChromaStyle(ChromaStyle(s, nil)).Style(s.Diff).TabWidth(4)
	return diff
}

// DiffSplitMinWidth is the narrowest diff, in cells, drawn side by side when
// the diff mode is auto. Below it each half would be too cramped to read.
const DiffSplitMinWidth = 160

// SplitDiff reports whether a diff of the given width is drawn side by side
// under the given diff mode: always for "split", never for "unified", and
// from DiffSplitMinWidth up for "auto" or an unset mode.
func SplitDiff(mode string, width int) bool {
	switch mode {
	case config.DiffModeSplit:
		return true
	case config.DiffModeUnified:
		return false
	default:
		return width >= DiffSplitMinWidth
	}
}
