// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): packages/plugin/src/hooks/magic-context/system-prompt-hash.ts, packages/plugin/src/features/magic-context/dreamer/scheduler.ts

package magiccontext

import (
	"context"
	"strings"
	"time"
)

// SystemPromptSection returns the Magic Context guidance to append to the
// agent's system prompt, or "" when injection is off or the prompt carries
// a skip mark.
func (e *Engine) SystemPromptSection(systemPrompt string, subagent bool) string {
	if !e.Enabled() || !e.Settings.InjectGuidance {
		return ""
	}
	for _, mark := range e.Settings.InjectSkipMarks {
		if mark != "" && strings.Contains(systemPrompt, mark) {
			return ""
		}
	}
	return BuildMagicContextSection(GuidanceOptions{
		CtxReduce:  e.Compaction(),
		Dreamer:    e.Settings.Dreamer && e.Dreamer != nil,
		SmartNotes: e.Settings.SmartNotes,
		Temporal:   e.Settings.TemporalAwareness,
		Subagent:   subagent,
		Language:   e.Settings.Language,
		Memory:     e.Settings.MemoryEnabled,
	})
}

// RunDreamTicker runs due dreamer tasks for the project every
// DreamTickInterval until ctx ends. It returns at once when the dreamer is
// off.
func (e *Engine) RunDreamTicker(ctx context.Context) {
	if !e.Enabled() || !e.Settings.Dreamer || e.ProjectKey == "" {
		return
	}
	tick := func() {
		deps := DreamDeps{
			Store: e.Store, Settings: e.Settings, ProjectPath: e.ProjectKey,
			Generator: e.Dreamer, Embedder: e.Embed, EmbeddingModelID: e.Settings.EmbeddingModel,
			Now: e.Now,
		}
		if _, err := RunDueDreams(ctx, deps); err != nil && ctx.Err() == nil {
			e.logf("magic-context: scheduled dream failed: %v", err)
		}
	}
	t := time.NewTicker(DreamTickInterval)
	defer t.Stop()
	tick()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
		}
	}
}
