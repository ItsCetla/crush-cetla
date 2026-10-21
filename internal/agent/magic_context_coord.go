// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): hooks/magic-context/hook.ts, plugin/tool-registry.ts.

package agent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/openrouter"
	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/magiccontext"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/pubsub"
)

// newMagicContextEngine builds the Magic Context engine for the main agents
// when it is enabled in the config. It returns nil when Magic Context is off
// or there is no database (agent tests).
func (c *coordinator) newMagicContextEngine(ctx context.Context, db *sql.DB) *magiccontext.Engine {
	opts := c.cfg.Config().Options
	if db == nil || opts == nil || !opts.MagicContextEnabled() {
		return nil
	}
	settings := magiccontext.SettingsFromConfig(opts.MagicContext)
	store := magiccontext.NewStore(db)
	logf := func(format string, args ...any) {
		slog.Debug(fmt.Sprintf(format, args...))
	}
	var embed magiccontext.Embedder
	if settings.EmbeddingEndpoint != "" {
		embed = magiccontext.NewHTTPEmbedder(settings.EmbeddingEndpoint, settings.EmbeddingModel, settings.EmbeddingAPIKey)
	}
	projectKey := magiccontext.ProjectKey(c.cfg.WorkingDir())

	historianWindow := 0
	if m, _, err := c.magicContextModel(ctx, settings.HistorianModel); err == nil {
		historianWindow = int(m.CatwalkCfg.ContextWindow)
	}

	e := &magiccontext.Engine{
		Store:      store,
		Settings:   settings,
		ProjectKey: projectKey,
		ProjectDir: c.cfg.WorkingDir(),
		Embed:      embed,
		Logf:       logf,
		Historian: &magiccontext.Historian{
			Store:         store,
			Settings:      settings,
			Generate:      c.magicContextGenerator(settings.HistorianModel),
			ContextWindow: historianWindow,
			ProjectKey:    projectKey,
			Embed:         embed,
			EmbedModelID:  settings.EmbeddingModel,
			Notify:        c.magicContextNotice,
			Logf:          logf,
		},
	}
	if settings.Dreamer {
		e.Dreamer = c.magicContextGenerator(settings.DreamerModel)
	}
	return e
}

// syncMagicContext builds or drops the Magic Context engine when the
// setting was turned on or off since the last call, and hands the result
// to the main agents, so the switch applies from their next step without
// a restart. Other Magic Context settings apply on the next start: a
// rebuild would lose the engine's in-flight historian runs. It reports
// whether the engine changed.
func (c *coordinator) syncMagicContext(ctx context.Context) bool {
	c.mcMu.Lock()
	defer c.mcMu.Unlock()
	on := c.cfg.Config().Options.MagicContextEnabled()
	if on == (c.mc.Load() != nil) {
		return false
	}
	e := c.newMagicContextEngine(ctx, c.mcDB)
	if on && e == nil {
		// No database to keep it in; stay off.
		return false
	}
	if c.mcStop != nil {
		c.mcStop()
		c.mcStop = nil
	}
	if e != nil {
		// The dreamer schedule lives until the engine is replaced or
		// the process ends.
		tickCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
		c.mcStop = stop
		go e.RunDreamTicker(tickCtx)
	}
	c.mc.Store(e)
	for _, a := range c.agents {
		if s, ok := a.(interface{ SetMagicContext(*magiccontext.Engine) }); ok {
			s.SetMagicContext(e)
		}
	}
	return true
}

// magicContextNotice shows a Magic Context notice for a session.
func (c *coordinator) magicContextNotice(sessionID, text string) {
	if c.notify == nil || strings.TrimSpace(text) == "" {
		return
	}
	c.notify.Publish(pubsub.CreatedEvent, notify.Notification{
		SessionID: sessionID,
		Type:      notify.TypeInfo,
		Message:   text,
	})
}

// magicContextModel resolves a side-model spec: "large" (or empty) is the
// session's large model, "small" the small model, and "provider/model" any
// configured model. It returns the model and its provider's system prompt
// prefix. Models resolve on every call so a model switch is picked up.
func (c *coordinator) magicContextModel(ctx context.Context, spec string) (Model, string, error) {
	cfg := c.cfg.Config()
	var sel config.SelectedModel
	switch spec = strings.TrimSpace(spec); spec {
	case "", "large":
		s, ok := cfg.Models[config.SelectedModelTypeLarge]
		if !ok {
			return Model{}, "", errLargeModelNotSelected
		}
		sel = s
	case "small":
		s, ok := cfg.Models[config.SelectedModelTypeSmall]
		if !ok {
			return Model{}, "", errSmallModelNotSelected
		}
		sel = s
	default:
		provider, model, ok := strings.Cut(spec, "/")
		if !ok || provider == "" || model == "" {
			return Model{}, "", fmt.Errorf("magic context: model %q must be large, small or provider/model", spec)
		}
		sel = config.SelectedModel{Provider: provider, Model: model}
	}

	providerCfg, ok := cfg.Providers.Get(sel.Provider)
	if !ok {
		return Model{}, "", fmt.Errorf("magic context: provider %q is not configured", sel.Provider)
	}
	catwalkModel := cfg.GetModel(sel.Provider, sel.Model)
	if catwalkModel == nil {
		return Model{}, "", fmt.Errorf("magic context: model %s/%s not found", sel.Provider, sel.Model)
	}
	provider, err := c.buildProvider(providerCfg, sel, true)
	if err != nil {
		return Model{}, "", err
	}
	modelID := sel.Model
	if sel.Provider == openrouter.Name && isExactoSupported(modelID) {
		modelID += ":exacto"
	}
	lm, err := provider.LanguageModel(ctx, modelID)
	if err != nil {
		return Model{}, "", err
	}
	lm = newRequestTimeoutModel(lm, cfg.Options.GetRequestTimeout())
	return Model{
		Model:      lm,
		CatwalkCfg: *catwalkModel,
		ModelCfg:   sel,
		FlatRate:   providerCfg.FlatRate,
	}, providerCfg.SystemPromptPrefix, nil
}

// magicContextGenerator returns a one-shot text generator for the historian
// and dreamer on the model named by spec.
func (c *coordinator) magicContextGenerator(spec string) magiccontext.Generator {
	return func(ctx context.Context, system, user string, maxTokens int) (string, error) {
		m, prefix, err := c.magicContextModel(ctx, spec)
		if err != nil {
			return "", err
		}
		tok := int64(maxTokens)
		if tok <= 0 {
			tok = m.CatwalkCfg.DefaultMaxTokens
		}
		opts := []fantasy.AgentOption{
			fantasy.WithSystemPrompt(system),
			fantasy.WithUserAgent(userAgent),
		}
		if tok > 0 {
			opts = append(opts, fantasy.WithMaxOutputTokens(tok))
		}
		resp, err := fantasy.NewAgent(m.Model, opts...).Stream(ctx, fantasy.AgentStreamCall{
			Prompt: user,
			PrepareStep: func(callCtx context.Context, o fantasy.PrepareStepFunctionOptions) (context.Context, fantasy.PrepareStepResult, error) {
				var prepared fantasy.PrepareStepResult
				prepared.Messages = o.Messages
				if prefix != "" {
					prepared.Messages = append([]fantasy.Message{fantasy.NewSystemMessage(prefix)}, prepared.Messages...)
				}
				return callCtx, prepared, nil
			},
		})
		if err != nil {
			return "", err
		}
		if resp == nil {
			return "", errors.New("magic context: empty model response")
		}
		text := resp.Response.Content.Text()
		text = thinkTagRegex.ReplaceAllString(text, "")
		text = orphanThinkTagRegex.ReplaceAllString(text, "")
		return strings.TrimSpace(text), nil
	}
}

// magicContextTools returns the ctx_* tools for a main agent: ctx_reduce
// when compaction is on, ctx_expand, ctx_note and ctx_search always, and
// ctx_memory when project memory is on.
func (c *coordinator) magicContextTools() []fantasy.AgentTool {
	e := c.mc.Load()
	if !e.Enabled() {
		return nil
	}
	sessionID := func(ctx context.Context) string { return tools.GetSessionFromContext(ctx) }
	projectKey := func(context.Context) string { return e.ProjectKey }
	var out []fantasy.AgentTool
	if e.Compaction() {
		out = append(out, magiccontext.NewCtxReduceTool(magiccontext.ReduceToolDeps{
			Store:     e.Store,
			SessionID: sessionID,
			InputTokens: func(ctx context.Context, sid string) int64 {
				meta, err := e.Store.Meta(ctx, sid)
				if err != nil {
					return 0
				}
				return meta.LastInputTokens
			},
		}))
	}
	out = append(out,
		magiccontext.NewCtxExpandTool(magiccontext.ExpandToolDeps{
			Store:     e.Store,
			SessionID: sessionID,
			Messages: func(ctx context.Context, sid string) ([]message.Message, error) {
				return c.messages.List(ctx, sid)
			},
		}),
		magiccontext.NewCtxNoteTool(magiccontext.NoteToolDeps{
			Store:       e.Store,
			Settings:    e.Settings,
			SessionID:   sessionID,
			ProjectPath: projectKey,
		}),
		magiccontext.NewCtxSearchTool(magiccontext.SearchToolDeps{
			Store:      e.Store,
			Settings:   e.Settings,
			ProjectKey: projectKey,
			SessionID:  sessionID,
			VisibleMemoryIDs: func(ctx context.Context) []int64 {
				meta, err := e.Store.Meta(ctx, tools.GetSessionFromContext(ctx))
				if err != nil {
					return nil
				}
				return meta.CachedM0MemoryIDs
			},
			Embedder:       e.Embed,
			EmbeddingModel: e.Settings.EmbeddingModel,
		}),
	)
	if e.Settings.MemoryEnabled {
		out = append(out, magiccontext.NewCtxMemoryTool(magiccontext.MemoryToolDeps{
			Store:          e.Store,
			Settings:       e.Settings,
			ProjectKey:     projectKey,
			SessionID:      sessionID,
			Embedder:       e.Embed,
			EmbeddingModel: e.Settings.EmbeddingModel,
		}))
	}
	return out
}

// mainMagicContext returns the engine for a main agent, nil for sub-agents.
func (c *coordinator) mainMagicContext(isSubAgent bool) *magiccontext.Engine {
	e := c.mc.Load()
	if isSubAgent || !e.Enabled() {
		return nil
	}
	return e
}

// MagicContextCommander is implemented by coordinators that can run the
// /ctx-* commands. It is a separate interface so test fakes of
// [Coordinator] don't need to implement it.
type MagicContextCommander interface {
	MagicContextCommand(ctx context.Context, sessionID, name, args string) (string, error)
}

var _ MagicContextCommander = (*coordinator)(nil)

// MagicContextCommand runs a /ctx-* command and returns its reply. The
// command never reaches the model; long-running commands post their
// progress and result as info notifications.
func (c *coordinator) MagicContextCommand(ctx context.Context, sessionID, name, args string) (string, error) {
	if sessionID == "" {
		return "", errors.New("no session")
	}
	msgs, err := c.messages.List(ctx, sessionID)
	if err != nil {
		return "", err
	}
	// The catalog window, or one learned from an overflow error.
	limit := int(c.Model().CatwalkCfg.ContextWindow)
	if sa, ok := c.currentAgent().(*sessionAgent); ok {
		limit = int(sa.contextWindow(c.Model()))
	}
	return c.mc.Load().RunCommand(ctx, magiccontext.CommandInput{
		Name:         name,
		Args:         args,
		SessionID:    sessionID,
		Messages:     msgs,
		ContextLimit: limit,
		Notify:       func(text string) { c.magicContextNotice(sessionID, text) },
	}), nil
}
