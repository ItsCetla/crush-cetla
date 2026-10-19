package styles

import "charm.land/lipgloss/v2"

// FieldGlyph is the texture drawn beside the logo, in the header and after
// dialog titles. Upstream uses "╱".
const FieldGlyph = "·"

// DefaultThemeName is the built-in theme used when none is configured.
const DefaultThemeName = "cetla"

// Default returns the default theme: warm ink with an ember accent, matching
// the cetla.dev palette.
func Default() Styles {
	return cetlaOverrides(quickStyle(cetlaOpts()))
}

// cetla palette. Names follow cetla.dev's CSS tokens where one exists.
var (
	cetlaInk0   = lipgloss.Color("#12100E") // --bg (dark)
	cetlaInk1   = lipgloss.Color("#1A1815") // --bg-2
	cetlaInk2   = lipgloss.Color("#211E1A")
	cetlaInk3   = lipgloss.Color("#2E2A25") // --line
	cetlaInk4   = lipgloss.Color("#433D35")
	cetlaStone  = lipgloss.Color("#635C52")
	cetlaDust   = lipgloss.Color("#8E867A") // --ink-3
	cetlaSand   = lipgloss.Color("#C9C1B4") // --ink-2
	cetlaPaper  = lipgloss.Color("#F4EFE6") // --ink
	cetlaWhite  = lipgloss.Color("#FFFBF4")
	cetlaEmber  = lipgloss.Color("#FF6A3D") // --accent
	cetlaFlame  = lipgloss.Color("#E8501F") // light-mode --accent
	cetlaAmber  = lipgloss.Color("#FFB347")
	cetlaHoney  = lipgloss.Color("#F5D08A")
	cetlaRust   = lipgloss.Color("#7A3A22")
	cetlaLive   = lipgloss.Color("#3DDC84") // --live
	cetlaMoss   = lipgloss.Color("#2FAF68")
	cetlaFern   = lipgloss.Color("#1F6B44")
	cetlaRed    = lipgloss.Color("#F0524F")
	cetlaCoral  = lipgloss.Color("#FF7F6E")
	cetlaSky    = lipgloss.Color("#7CB8F0")
	cetlaHarbor = lipgloss.Color("#4E86BF")
	cetlaDeep   = lipgloss.Color("#2A4561")
	cetlaRose   = lipgloss.Color("#F29E9E")
	cetlaLilac  = lipgloss.Color("#C49BFF")
	cetlaTeal   = lipgloss.Color("#5CD6C8")
)

func cetlaOpts() quickStyleOpts {
	return quickStyleOpts{
		primary:   cetlaEmber,
		secondary: cetlaAmber,
		accent:    cetlaEmber,
		keyword:   cetlaRose,

		fgBase:       cetlaPaper,
		fgMoreSubtle: cetlaSand,
		fgSubtle:     cetlaDust,
		fgMostSubtle: cetlaStone,

		onPrimary: cetlaInk0,

		bgBase:         cetlaInk0,
		bgLeastVisible: cetlaInk1,
		bgLessVisible:  cetlaInk2,
		bgMostVisible:  cetlaInk3,

		separator: cetlaInk3,

		destructive:       cetlaCoral,
		error:             cetlaRed,
		warningSubtle:     cetlaHoney,
		warning:           cetlaAmber,
		attention:         cetlaEmber,
		busy:              cetlaHoney,
		info:              cetlaSky,
		infoMoreSubtle:    cetlaHarbor,
		infoMostSubtle:    cetlaDeep,
		success:           cetlaLive,
		successMoreSubtle: cetlaMoss,
		successMostSubtle: cetlaFern,
		yolo:              cetlaHoney,
		plan:              cetlaAmber,
		planMoreSubtle:    cetlaRust,

		diffInsertFg: lipgloss.Color("#629657"),
		diffDeleteFg: lipgloss.Color("#a45c59"),

		button:         cetlaEmber,
		buttonSubtle:   cetlaInk2,
		buttonInactive: cetlaInk3,
		buttonHovered:  cetlaInk4,

		ansiBlack:   cetlaInk1,
		ansiRed:     cetlaRed,
		ansiGreen:   cetlaMoss,
		ansiYellow:  cetlaAmber,
		ansiBlue:    cetlaSky,
		ansiMagenta: cetlaLilac,
		ansiCyan:    cetlaTeal,
		ansiWhite:   cetlaSand,

		ansiBrightBlack:   cetlaStone,
		ansiBrightRed:     cetlaCoral,
		ansiBrightGreen:   cetlaLive,
		ansiBrightYellow:  cetlaHoney,
		ansiBrightBlue:    lipgloss.Color("#A8D1FF"),
		ansiBrightMagenta: cetlaRose,
		ansiBrightCyan:    lipgloss.Color("#9BEDE2"),
		ansiBrightWhite:   cetlaWhite,
	}
}

// cetlaOverrides tunes the few styles the token model doesn't cover.
func cetlaOverrides(s Styles) Styles {
	// Logo and header: amber -> ember gradient on a quiet field.
	s.Logo.FieldColor = cetlaStone
	s.Logo.CharmColor = cetlaPaper
	s.Logo.VersionColor = cetlaDust
	s.Logo.SmallCharm = s.Logo.SmallCharm.Foreground(cetlaPaper)
	s.Logo.SmallDiagonals = s.Logo.SmallDiagonals.Foreground(cetlaStone)
	s.Header.Charm = s.Header.Charm.Foreground(cetlaPaper)
	s.Header.Diagonals = s.Header.Diagonals.Foreground(cetlaStone)

	// Editor prompt: ember caret over rust dots instead of green.
	s.Editor.PromptNormalIconFocused = s.Editor.PromptNormalIconFocused.Foreground(cetlaEmber)
	s.Editor.PromptNormalFocused = s.Editor.PromptNormalFocused.Foreground(cetlaRust)
	return s
}
