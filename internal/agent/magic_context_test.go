// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): integration coverage for packages/plugin/src/hooks/magic-context/transform.ts wiring

package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/magiccontext"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

// mcRecordingModel streams a fixed reply with usage and records every
// prompt it was sent. It never talks to a real provider.
type mcRecordingModel struct {
	reply string
	usage fantasy.Usage

	mu      sync.Mutex
	prompts []fantasy.Prompt
	tools   [][]string
}

func (m *mcRecordingModel) Provider() string { return "fake" }
func (m *mcRecordingModel) Model() string    { return "fake-model" }

func (m *mcRecordingModel) Generate(context.Context, fantasy.Call) (*fantasy.Response, error) {
	return &fantasy.Response{
		Content:      fantasy.ResponseContent{fantasy.TextContent{Text: "title"}},
		FinishReason: fantasy.FinishReasonStop,
	}, nil
}

func (m *mcRecordingModel) Stream(_ context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	m.mu.Lock()
	m.prompts = append(m.prompts, call.Prompt)
	var names []string
	for _, tl := range call.Tools {
		names = append(names, tl.GetName())
	}
	m.tools = append(m.tools, names)
	m.mu.Unlock()
	reply, usage := m.reply, m.usage
	return func(yield func(fantasy.StreamPart) bool) {
		if !yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextStart, ID: "1"}) {
			return
		}
		if !yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, ID: "1", Delta: reply}) {
			return
		}
		if !yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextEnd, ID: "1"}) {
			return
		}
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonStop, Usage: usage})
	}, nil
}

func (m *mcRecordingModel) GenerateObject(context.Context, fantasy.ObjectCall) (*fantasy.ObjectResponse, error) {
	return nil, errors.New("not implemented")
}

func (m *mcRecordingModel) StreamObject(context.Context, fantasy.ObjectCall) (fantasy.ObjectStreamResponse, error) {
	return nil, errors.New("not implemented")
}

func (m *mcRecordingModel) lastPromptText() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.prompts) == 0 {
		return ""
	}
	var b strings.Builder
	for _, msg := range m.prompts[len(m.prompts)-1] {
		if msg.Role == fantasy.MessageRoleSystem {
			continue
		}
		for _, p := range msg.Content {
			if tp, ok := p.(fantasy.TextPart); ok {
				b.WriteString(tp.Text)
				b.WriteString("\n")
			}
		}
	}
	return b.String()
}

type mcTestEnv struct {
	agent    *sessionAgent
	model    *mcRecordingModel
	engine   *magiccontext.Engine
	sessions session.Service
	messages message.Service
}

func newMagicContextTestAgent(t *testing.T, enabled bool) mcTestEnv {
	t.Helper()
	conn, err := db.Connect(t.Context(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	q := db.New(conn)
	sessions := session.NewService(q, conn)
	messages := message.NewService(q)

	var engine *magiccontext.Engine
	var tools []fantasy.AgentTool
	if enabled {
		set := magiccontext.SettingsFromConfig(&config.MagicContextOptions{Enabled: true})
		set.AutoSearch = false
		now := time.Unix(1_800_000_000, 0)
		// No historian: the test must never reach a real model.
		engine = &magiccontext.Engine{
			Store:    magiccontext.NewStore(conn),
			Settings: set,
			Now:      func() time.Time { return now },
		}
		// ctx_reduce is what makes §N§ tags visible to the model.
		tools = append(tools, magiccontext.NewCtxReduceTool(magiccontext.ReduceToolDeps{
			Store:     engine.Store,
			SessionID: func(context.Context) string { return "" },
		}))
	}

	model := &mcRecordingModel{
		reply: "§7§ all done",
		usage: fantasy.Usage{InputTokens: 50000, OutputTokens: 20},
	}
	sa := testSessionAgentWithMC(sessions, messages, model, engine, tools)
	return mcTestEnv{agent: sa, model: model, engine: engine, sessions: sessions, messages: messages}
}

func testSessionAgentWithMC(sessions session.Service, messages message.Service, model fantasy.LanguageModel, engine *magiccontext.Engine, tools []fantasy.AgentTool) *sessionAgent {
	m := Model{Model: model}
	m.CatwalkCfg.ContextWindow = 200000
	m.CatwalkCfg.DefaultMaxTokens = 10000
	m.ModelCfg.Provider = "fake"
	m.ModelCfg.Model = "fake-model"
	return NewSessionAgent(SessionAgentOptions{
		LargeModel:   m,
		SmallModel:   m,
		SystemPrompt: "system",
		IsYolo:       true,
		Sessions:     sessions,
		Messages:     messages,
		MagicContext: engine,
		Tools:        tools,
	}).(*sessionAgent)
}

func TestMagicContextRunTagsPromptAndRecordsUsage(t *testing.T) {
	t.Parallel()
	env := newMagicContextTestAgent(t, true)
	sess, err := env.sessions.Create(t.Context(), "mc")
	require.NoError(t, err)

	_, err = env.agent.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "hello there"})
	require.NoError(t, err)

	// The prompt went through the transform: the user turn carries a §N§ tag.
	prompt := env.model.lastPromptText()
	require.Contains(t, prompt, "hello there")
	require.Regexp(t, `§\d+§ hello there`, prompt)
	env.model.mu.Lock()
	require.Contains(t, env.model.tools[len(env.model.tools)-1], magiccontext.CtxReduceToolName)
	env.model.mu.Unlock()

	// The tag the model echoed is stripped before the reply is stored.
	msgs, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	var reply string
	for _, m := range msgs {
		if m.Role == message.Assistant {
			reply = m.Content().String()
		}
	}
	require.Equal(t, "all done", strings.TrimSpace(reply))

	// The response size is recorded for the next pass.
	meta, err := env.engine.Store.Meta(t.Context(), sess.ID)
	require.NoError(t, err)
	require.EqualValues(t, 50000, meta.LastInputTokens)
	require.InDelta(t, 25, meta.LastContextPercentage, 1e-9)

	// Magic Context without a historian leaves auto-summarize in charge.
	require.True(t, env.agent.magicContextActive())
	require.False(t, env.agent.magicContextCompaction())
}

func TestMagicContextDisabledLeavesPromptAlone(t *testing.T) {
	t.Parallel()
	env := newMagicContextTestAgent(t, false)
	sess, err := env.sessions.Create(t.Context(), "plain")
	require.NoError(t, err)

	_, err = env.agent.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "hello there"})
	require.NoError(t, err)

	require.NotContains(t, env.model.lastPromptText(), "§")
	require.False(t, env.agent.magicContextActive())

	msgs, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	var reply string
	for _, m := range msgs {
		if m.Role == message.Assistant {
			reply = m.Content().String()
		}
	}
	require.Equal(t, "§7§ all done", reply, "without Magic Context the reply is stored verbatim")
}

func TestMagicContextSubAgentStaysPlain(t *testing.T) {
	t.Parallel()
	env := newMagicContextTestAgent(t, true)
	env.agent.isSubAgent = true
	require.False(t, env.agent.magicContextActive())
}

func TestMagicContextSwitchesLive(t *testing.T) {
	t.Parallel()
	env := newMagicContextTestAgent(t, true)
	require.True(t, env.agent.magicContextActive())

	env.agent.SetMagicContext(nil)
	require.False(t, env.agent.magicContextActive())
	sess, err := env.sessions.Create(t.Context(), "off")
	require.NoError(t, err)
	_, err = env.agent.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "hello there"})
	require.NoError(t, err)
	require.NotContains(t, env.model.lastPromptText(), "§")

	env.agent.SetMagicContext(env.engine)
	require.True(t, env.agent.magicContextActive())
}

func TestCoordinatorSyncMagicContext(t *testing.T) {
	env := testEnv(t)
	cfg, err := config.Init(env.workingDir, "", false)
	require.NoError(t, err)
	conn, err := db.Connect(t.Context(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	sa := testSessionAgentWithMC(nil, nil, &mcRecordingModel{}, nil, nil)
	c := &coordinator{cfg: cfg, mcDB: conn, agents: map[string]SessionAgent{config.AgentCoder: sa}}
	opts := cfg.Config().Options

	require.False(t, c.syncMagicContext(t.Context()), "off stays off")
	require.Nil(t, c.mc.Load())

	opts.MagicContext = &config.MagicContextOptions{Enabled: true}
	require.True(t, c.syncMagicContext(t.Context()))
	e := c.mc.Load()
	require.NotNil(t, e)
	require.Same(t, e, sa.mc.Load(), "the agent gets the new engine")
	require.False(t, c.syncMagicContext(t.Context()), "no change, no rebuild")
	require.Same(t, e, c.mc.Load())

	opts.MagicContext.Enabled = false
	require.True(t, c.syncMagicContext(t.Context()))
	require.Nil(t, c.mc.Load())
	require.Nil(t, sa.mc.Load())
	require.Nil(t, c.mcStop, "the dreamer schedule stops with the engine")
}
