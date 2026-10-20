// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): index.ts, features/magic-context/index.ts.

// Package magiccontext is a native Go port of Magic Context
// (https://github.com/cortexkit/magic-context), the context-management system
// for long-running coding agents by Ufuk Altinok, released under the MIT
// License. The upstream license is kept next to this file in LICENSE, and the
// historian prompts and calibration seeds in prompts/ are copied verbatim
// from the upstream project.
//
// Magic Context replaces one-shot auto-summarize with a rolling history:
//
//   - A background "historian" model turns old raw messages into ordered
//     compartments with four paraphrase tiers (p1-p4) and an importance score.
//   - Each pass renders the compartments into a stable <session-history>
//     block (m0) with a decay curve, plus a small "since" block (m1), and
//     hides the raw messages the compartments cover.
//   - Every message text and tool output gets a §N§ tag so the agent can
//     stamp spent outputs with ctx_reduce and recover them with ctx_expand.
//   - Project memories, search, notes and the dreamer build durable
//     knowledge on top of that.
//
// The feature is opt-in (options.magic_context.enabled) and off by default.
package magiccontext
