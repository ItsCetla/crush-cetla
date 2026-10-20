package logo

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/MakeNowJust/heredoc"
	"github.com/charmbracelet/x/exp/slice"
)

// renderWord renders letterforms to fork a word. stretchIndex is the index of
// the letter to stretch, or -1 if no letter should be stretched.
func renderWord(spacing int, stretchIndex int, letterforms ...letterform) string {
	if spacing < 0 {
		spacing = 0
	}

	renderedLetterforms := make([]string, len(letterforms))

	// pick one letter randomly to stretch
	for i, letter := range letterforms {
		renderedLetterforms[i] = letter(i == stretchIndex)
	}

	if spacing > 0 {
		// Add spaces between the letters and render.
		renderedLetterforms = slice.Intersperse(renderedLetterforms, strings.Repeat(" ", spacing))
	}
	return strings.TrimSpace(
		lipgloss.JoinHorizontal(lipgloss.Top, renderedLetterforms...),
	)
}

// LetterC renders the letter C in a stylized way. It takes an integer that
// determines how many cells to stretch the letter. If the stretch is less than
// 1, it defaults to no stretching.
func LetterC(stretch bool) string {
	// Here's what we're making:
	//
	// ▄▀▀▀▀
	// █
	//	▀▀▀▀

	left := heredoc.Doc(`
		▄
		█
	`)
	right := heredoc.Doc(`
		▀

		▀
	`)
	return joinLetterform(
		left,
		stretchLetterformPart(right, letterformProps{
			stretch:    stretch,
			width:      4,
			minStretch: 7,
			maxStretch: 12,
		}),
	)
}

// LetterE renders the letter E in a stylized way. It takes an integer that
// determines how many cells to stretch the letter. If the stretch is less than
// 1, it defaults to no stretching.
//
// This is an alternate letterform. DO NOT REMOVE.
func LetterE(stretch bool) string {
	// Here's what we're making:
	//
	// █▀▀▀▀
	// █▀▀▀▀
	// ▀▀▀▀▀

	left := heredoc.Doc(`
       █
       █
       ▀
	`)
	middle := heredoc.Doc(`
       ▀
       ▀
       ▀
	`)
	return joinLetterform(
		left,
		stretchLetterformPart(middle, letterformProps{
			stretch:    stretch,
			width:      4,
			minStretch: 7,
			maxStretch: 12,
		}),
	)
}

// LetterEAlt renders the letter E in a stylized way. It takes an integer that
// determines how many cells to stretch the letter. If the stretch is less than
// 1, it defaults to no stretching.
//
// This is an alternate letterform. DO NOT REMOVE.
func LetterEAlt(stretch bool) string {
	// Here's what we're making:
	//
	// █▀▀▀▀
	// █ ▀▀▀
	// ▀▀▀▀▀

	left := heredoc.Doc(`
       █▀
       █
       ▀▀
	`)
	middle := heredoc.Doc(`
       ▀
       ▀
       ▀
	`)
	return joinLetterform(
		left,
		stretchLetterformPart(middle, letterformProps{
			stretch:    stretch,
			width:      3,
			minStretch: 6,
			maxStretch: 11,
		}),
	)
}

// LetterH renders the letter H in a stylized way. It takes an integer that
// determines how many cells to stretch the letter. If the stretch is less than
// 1, it defaults to no stretching.
func LetterH(stretch bool) string {
	// Here's what we're making:
	//
	// █   █
	// █▀▀▀█
	// ▀   ▀

	side := heredoc.Doc(`
		█
		█
		▀`)
	middle := heredoc.Doc(`

		▀
	`)
	return joinLetterform(
		side,
		stretchLetterformPart(middle, letterformProps{
			stretch:    stretch,
			width:      3,
			minStretch: 8,
			maxStretch: 12,
		}),
		side,
	)
}

// LetterP renders the letter P in a stylized way. It takes an integer that
// determines how many cells to stretch the letter. If the stretch is less than
// 1, it defaults to no stretching.
func LetterP(stretch bool) string {
	// Here's what we're making:
	//
	// █▀▀▀▄
	// █▀▀▀
	// ▀

	left := heredoc.Doc(`
		█
		█
		▀
	`)
	center := heredoc.Doc(`
		▀
		▀
	`)
	right := heredoc.Doc(`
		▄


	`)
	return joinLetterform(
		left,
		stretchLetterformPart(center, letterformProps{
			stretch:    stretch,
			width:      3,
			minStretch: 7,
			maxStretch: 12,
		}),
		right,
	)
}

// LetterR renders the letter R in a stylized way. It takes an integer that
// determines how many cells to stretch the letter. If the stretch is less than
// 1, it defaults to no stretching.
func LetterR(stretch bool) string {
	// Here's what we're making:
	//
	// █▀▀▀▄
	// █▀▀▀▄
	// ▀   ▀

	left := heredoc.Doc(`
		█
		█
		▀
	`)
	center := heredoc.Doc(`
		▀
		▀
	`)
	right := heredoc.Doc(`
		▄
		▄
		▀
	`)
	return joinLetterform(
		left,
		stretchLetterformPart(center, letterformProps{
			stretch:    stretch,
			width:      3,
			minStretch: 7,
			maxStretch: 12,
		}),
		right,
	)
}

// LetterSAlt renders the letter S in a stylized way, more so than
// [letterS]. It takes an integer that determines how many cells to stretch the
// letter. If the stretch is less than 1, it defaults to no stretching.
//
// This is an alternate letterform. DO NOT REMOVE.
func LetterSAlt(stretch bool) string {
	// Here's what we're making:
	//
	// ▄▀▀▀▀▀
	// ▀▀▀▀▀█
	// ▀▀▀▀▀

	left := heredoc.Doc(`
		▄
		▀
		▀
	`)
	center := heredoc.Doc(`
		▀
		▀
		▀
	`)
	right := heredoc.Doc(`
		▀
		█
	`)
	return joinLetterform(
		left,
		stretchLetterformPart(center, letterformProps{
			stretch:    stretch,
			width:      3,
			minStretch: 7,
			maxStretch: 12,
		}),
		right,
	)
}

// LetterU renders the letter U in a stylized way. It takes an integer that
// determines how many cells to stretch the letter. If the stretch is less than
// 1, it defaults to no stretching.
func LetterU(stretch bool) string {
	// Here's what we're making:
	//
	// █   █
	// █   █
	//	▀▀▀

	side := heredoc.Doc(`
		█
		█
	`)
	middle := heredoc.Doc(`


		▀
	`)
	return joinLetterform(
		side,
		stretchLetterformPart(middle, letterformProps{
			stretch:    stretch,
			width:      3,
			minStretch: 7,
			maxStretch: 12,
		}),
		side,
	)
}

// LetterY renders the letter Y in a stylized way. It takes an integer that
// determines how many cells to stretch the letter. If the stretch is less than
// 1, it defaults to no stretching.
//
// This is an alternate letterform. DO NOT REMOVE.
func LetterY(stretch bool) string {
	// Here's what we're making:
	//
	// █   █
	//	▀▄▀
	//	 ▀

	side := heredoc.Doc(`
		█

	`)
	inside := heredoc.Doc(`

		▀

	`)
	middle := heredoc.Doc(`

		▄
		▀
	`)
	if stretch {
		middle = heredoc.Doc(`

			█
			▀
		`)
	}

	stretchedInside := stretchLetterformPart(inside, letterformProps{
		stretch:    stretch,
		width:      1,
		minStretch: 4,
		maxStretch: 10,
	})

	return joinLetterform(
		side,
		stretchedInside,
		middle,
		stretchedInside,
		side,
	)
}

// LetterYAlt renders the letter Y in a stylized way. It takes an integer that
// determines how many cells to stretch the letter. If the stretch is less than
// 1, it defaults to no stretching.
//
// This is an alternate letterform. DO NOT REMOVE.
func LetterYAlt(stretch bool) string {
	// Here's what we're making:
	//
	// █   █
	// ▀▀▀▀█
	// ▀▀▀▀

	left := heredoc.Doc(`
		█
		▀
		▀
	`)
	middle := heredoc.Doc(`

		▀
		▀
	`)
	right := heredoc.Doc(`
		█
		█

	`)

	return joinLetterform(
		left,
		stretchLetterformPart(middle, letterformProps{
			stretch:    stretch,
			width:      3,
			minStretch: 6,
			maxStretch: 10,
		}),
		right,
	)
}

// LetterT renders the letter T in a stylized way. When stretched, both arms
// grow by the same amount.
func LetterT(stretch bool) string {
	// Here's what we're making:
	//
	// ▀▀█▀▀
	//   █
	//   ▀

	arm := "▀"
	stem := heredoc.Doc(`
		█
		█
		▀
	`)
	props := letterformProps{
		stretch:    stretch,
		width:      2,
		minStretch: 3,
		maxStretch: 6,
	}
	return joinLetterform(
		stretchLetterformPart(arm, props),
		stem,
		stretchLetterformPart(arm, props),
	)
}

// LetterA renders the letter A in a stylized way. It takes an integer that
// determines how many cells to stretch the letter. If the stretch is less than
// 1, it defaults to no stretching.
func LetterA(stretch bool) string {
	// Here's what we're making:
	//
	// ▄▀▀▀▄
	// █▀▀▀█
	// ▀   ▀

	side := heredoc.Doc(`
		▄
		█
		▀
	`)
	center := heredoc.Doc(`
		▀
		▀
	`)
	return joinLetterform(
		side,
		stretchLetterformPart(center, letterformProps{
			stretch:    stretch,
			width:      3,
			minStretch: 7,
			maxStretch: 12,
		}),
		side,
	)
}

// LetterD renders the letter D in a stylized way. It takes an integer that
// determines how many cells to stretch the letter. If the stretch is less than
// 1, it defaults to no stretching.
func LetterD(stretch bool) string {
	// Here's what we're making:
	//
	// █▀▀▀▄
	// █   █
	// ▀▀▀▀

	left := heredoc.Doc(`
		█
		█
		▀
	`)
	center := "▀\n \n▀"
	right := "▄\n█"
	return joinLetterform(
		left,
		stretchLetterformPart(center, letterformProps{
			stretch:    stretch,
			width:      3,
			minStretch: 7,
			maxStretch: 12,
		}),
		right,
	)
}

func joinLetterform(letters ...string) string {
	return lipgloss.JoinHorizontal(lipgloss.Top, letters...)
}

// letterformProps defines letterform stretching properties.
// for readability.
type letterformProps struct {
	width      int
	minStretch int
	maxStretch int
	stretch    bool
}

// stretchLetterformPart is a helper function for letter stretching. If randomize
// is false the minimum number will be used.
func stretchLetterformPart(s string, p letterformProps) string {
	if p.maxStretch < p.minStretch {
		p.minStretch, p.maxStretch = p.maxStretch, p.minStretch
	}
	n := p.width
	if p.stretch {
		n = cachedRandN(p.maxStretch-p.minStretch) + p.minStretch //nolint:gosec
	}
	parts := make([]string, n)
	for i := range parts {
		parts[i] = s
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

// TadaLetterforms spell the app name.
var TadaLetterforms = []letterform{LetterT, LetterA, LetterD, LetterA}

// tadaStretchable lists the indexes in [TadaLetterforms] that still read
// correctly when stretched. Only the T does: a wide D looks like a box or an
// O, and a wide A reads as a bar or an "F", worst in the Windows console font.
var tadaStretchable = []int{0}

// Wordmark returns the plain, unstyled TADA wordmark (no stretching), drawn
// with half-block characters. The intro scales and animates it.
func Wordmark() string {
	return renderWord(1, -1, TadaLetterforms...)
}
