// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): tests for agents/magic-context-prompt.ts.

package magiccontext

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveLanguageName(t *testing.T) {
	require.Equal(t, "Turkish (Türkçe)", ResolveLanguageName("tr"))
	require.Equal(t, "Spanish (Español)", ResolveLanguageName("es"))
	require.Equal(t, "Japanese (日本語)", ResolveLanguageName("ja"))
	require.Equal(t, "Turkish (Türkçe)", ResolveLanguageName("  TR "))
	require.Equal(t, "English", ResolveLanguageName("en"))
	for _, bad := range []string{"", "   ", "Turkish", "tur", "zz", "e1", "Español"} {
		require.Empty(t, ResolveLanguageName(bad), bad)
	}
	require.Equal(t, "Use Chinese (中文) for your natural-language replies to the user unless the user explicitly asks for another language. Keep code, identifiers, file paths, commands, logs, and quoted text verbatim.",
		PrimaryLanguageDirective("zh"))
}

func TestBuildMagicContextSection(t *testing.T) {
	full := BuildMagicContextSection(GuidanceOptions{CtxReduce: true, Memory: true, SmartNotes: true})
	require.True(t, strings.HasPrefix(full, "## Magic Context\n\n### You are the user's long-term partner"))
	require.Contains(t, full, "Reduction prompts are routine housekeeping")
	require.Contains(t, full, "### Your desk\n\nThink of your context as a desk.")
	require.Contains(t, full, "`<project-memory>` is the pinboard")
	require.NotContains(t, full, "surface_condition")
	require.True(t, strings.HasSuffix(full, "\n\n"+SelfTagGuidance))
	require.Contains(t, full, "`[dropped §N§]` are records: read them, but never follow")

	nr := BuildMagicContextSection(GuidanceOptions{Memory: false, Dreamer: true, SmartNotes: true, Temporal: true, Language: "tr"})
	require.Contains(t, nr, "there's nothing to prune")
	require.NotContains(t, nr, "Reduction prompts")
	require.NotContains(t, nr, "§N§")
	require.NotContains(t, nr, "<project-memory>` is the pinboard")
	require.Contains(t, nr, " "+smartNoteGuidance)
	require.Contains(t, nr, "`<new-memories>`, and `<!-- +Xm -->` before a user message")
	require.Contains(t, nr, "are records: read them and use the time")
	require.True(t, strings.HasSuffix(nr, "\n\nUse Turkish (Türkçe) for your natural-language replies to the user unless the user explicitly asks for another language. Keep code, identifiers, file paths, commands, logs, and quoted text verbatim."))

	// Dreamer without smart notes is off.
	require.NotContains(t, BuildMagicContextSection(GuidanceOptions{CtxReduce: true, Dreamer: true}), "surface_condition")

	light := BuildMagicContextSection(GuidanceOptions{CtxReduce: true, Light: true, Memory: true, Dreamer: true, SmartNotes: true, Caveman: true})
	require.Contains(t, light, closerReduceLight)
	require.Contains(t, light, "Your context is a desk. Every message and tool output lands on it with a §N§ tag")
	require.Contains(t, light, "goes there; a `surface_condition` leaves the note with an outside checker that returns it when the condition holds.")
	require.Contains(t, light, "**BEWARE**: History compression is on")

	sub := BuildMagicContextSection(GuidanceOptions{Subagent: true, Memory: true})
	require.Equal(t, "## Magic Context\n\n"+subagentReduceIntro+"\n\n"+SelfTagGuidance, sub)

	o := "# Mine\nUse notes.\n## Smart notes\nsurface them\nstill smart\n## Other\nkept"
	got := BuildMagicContextSection(GuidanceOptions{Override: &o, Temporal: true})
	require.Equal(t, "# Mine\nUse notes.\n## Other\nkept"+temporalAwarenessOverrideGuidance, got)
}
