package model

import (
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/session"
)

// newAmbientTestUI is newStatusTestUI with ambient mode on.
func newAmbientTestUI(t *testing.T) *UI {
	t.Helper()
	u, _ := newStatusTestUI(t, "Alpha", "Beta")
	u.ambient = newAmbientState(true)
	return u
}

// toolCallMsg is an assistant message in sessionID carrying one tool call.
func toolCallMsg(sessionID, id, name, input string, finished bool) pubsub.Event[message.Message] {
	return pubsub.Event[message.Message]{
		Type: pubsub.UpdatedEvent,
		Payload: message.Message{
			ID:        "m-" + id,
			SessionID: sessionID,
			Role:      message.Assistant,
			Parts: []message.ContentPart{message.ToolCall{
				ID: id, Name: name, Input: input, Finished: finished,
			}},
		},
	}
}

// startAmbientTurn starts a turn in the viewed session "sa".
func startAmbientTurn(t *testing.T, u *UI) {
	t.Helper()
	ev := created("sa", "m1", message.User)
	u.trackTabStatus(ev)
	require.NotNil(t, u.trackAmbient(ev), "a working line ticks")
}

// ambientRow draws the UI's ambient line and returns the editor's top row.
func ambientRow(u *UI) string {
	scr := uv.NewScreenBuffer(u.width, u.height)
	u.drawAmbient(scr, u.layout.editor)
	lines := strings.Split(ansi.Strip(scr.Render()), "\n")
	return lines[u.layout.editor.Min.Y]
}

func TestAmbientClassify(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]ambientActivity{
		"edit":     ambientEditing,
		"write":    ambientEditing,
		"bash":     ambientRunning,
		"view":     ambientReading,
		"grep":     ambientReading,
		"agent":    ambientDelegating,
		"todos":    ambientThinking,
		"whatever": ambientThinking,
	} {
		require.Equal(t, want, ambientClassify(name), name)
	}
}

func TestAmbientLabel(t *testing.T) {
	t.Parallel()
	require.Equal(t, "editing ui.go", ambientLabel("edit", `{"file_path":"/a/b/ui.go"}`))
	require.Equal(t, "running go test", ambientLabel("bash", `{"command":"go  test\n"}`))
	require.Equal(t, "running Run the tests", ambientLabel("bash", `{"command":"go test","description":"Run the tests"}`))
	require.Equal(t, "searching foo", ambientLabel("grep", `{"pattern":"foo"}`))
	require.Equal(t, "reading", ambientLabel("view", `{"file_pa`), "a streaming input shows only the verb")
	require.Equal(t, "using mystery", ambientLabel("mystery", ""))
	long := ambientLabel("bash", `{"command":"`+strings.Repeat("x", 500)+`"}`)
	require.LessOrEqual(t, len(long), len("running ")+ambientMaxLabel)
}

func TestJSONStringField(t *testing.T) {
	t.Parallel()
	require.Equal(t, "a.go", jsonStringField(`{"path": "a.go"}`, "path"))
	require.Equal(t, `say "hi"`, jsonStringField(`{"q":"say \"hi\""}`, "q"))
	require.Equal(t, "a b", jsonStringField(`{"q":"a\nb"}`, "q"))
	require.Equal(t, "half", jsonStringField(`{"q":"half`, "q"), "a value still streaming")
	require.Empty(t, jsonStringField(`{"q":1}`, "q"))
	require.Empty(t, jsonStringField(`{"q"`, "q"))
	require.Empty(t, jsonStringField(`{}`, "q"))
}

func TestAmbientFollowsTurn(t *testing.T) {
	t.Parallel()
	u := newAmbientTestUI(t)
	now := time.Now()

	u.trackAmbient(nil)
	require.False(t, u.ambient.visible(now), "no line while idle")

	startAmbientTurn(t, u)
	require.True(t, u.ambient.on)
	require.Equal(t, "thinking", u.ambient.label)

	u.trackAmbient(toolCallMsg("sa", "t1", "edit", `{"file_path":"/x/ui.go"}`, false))
	require.Equal(t, ambientEditing, u.ambient.activity)
	require.Equal(t, "editing ui.go", u.ambient.label)

	// The newest call's label fills in as its input streams; an older
	// call updating does not take it back.
	u.trackAmbient(toolCallMsg("sa", "t2", "bash", `{"command":"go`, false))
	u.trackAmbient(toolCallMsg("sa", "t2", "bash", `{"command":"go vet"}`, false))
	require.Equal(t, "running go vet", u.ambient.label)
	u.trackAmbient(toolCallMsg("sa", "t1", "edit", `{"file_path":"/x/ui.go"}`, true))
	require.Equal(t, "running go vet", u.ambient.label)
	require.Equal(t, ambientRunning, u.ambient.activity)

	// Another session's messages are ignored.
	u.trackAmbient(toolCallMsg("sb", "t9", "view", `{"file_path":"z.go"}`, false))
	require.Equal(t, "running go vet", u.ambient.label)

	u.trackTabStatus(turnFinished("sa"))
	settle(t, u, "sa")
	u.trackAmbient(nil)
	require.False(t, u.ambient.on)
	require.Equal(t, ambientEndDone, u.ambient.end)
	require.True(t, u.ambient.visible(time.Now()), "the line fades out")
	require.False(t, u.ambient.visible(time.Now().Add(ambientFadeOut)), "then it is gone")
}

func TestAmbientFailedTurn(t *testing.T) {
	t.Parallel()
	u := newAmbientTestUI(t)
	startAmbientTurn(t, u)

	u.trackTabStatus(pubsub.Event[notify.RunComplete]{Payload: notify.RunComplete{SessionID: "sa", Error: "boom"}})
	settle(t, u, "sa")
	u.trackAmbient(nil)
	require.Equal(t, ambientEndFailed, u.ambient.end)
	require.Equal(t, "failed", u.ambient.label)
}

func TestAmbientSessionSwitchResets(t *testing.T) {
	t.Parallel()
	u := newAmbientTestUI(t)
	startAmbientTurn(t, u)

	u.session = &session.Session{ID: "sb", Title: "Beta"}
	u.trackAmbient(nil)
	require.Equal(t, "sb", u.ambient.session)
	require.False(t, u.ambient.visible(time.Now()), "an idle session shows no line")

	u.session = &session.Session{ID: "sa", Title: "Alpha"}
	u.trackAmbient(nil)
	require.True(t, u.ambient.on, "back on the working session the line returns")
}

func TestAmbientDraw(t *testing.T) {
	t.Parallel()
	u := newAmbientTestUI(t)
	require.NotContains(t, ambientRow(u), "─", "nothing draws while idle")

	startAmbientTurn(t, u)
	u.trackAmbient(toolCallMsg("sa", "t1", "edit", `{"file_path":"/x/ui.go"}`, false))
	// Skip the fade in.
	u.ambient.start = time.Now().Add(-time.Minute)
	row := ambientRow(u)
	require.Contains(t, row, "───")
	require.Contains(t, row, "editing ui.go")
	require.LessOrEqual(t, ansi.StringWidth(strings.TrimRight(row, " ")), u.width)

	u.attachments.Update(message.Attachment{FileName: "a.png"})
	require.NotContains(t, ambientRow(u), "─", "attachments keep the row")
}

func TestAmbientOff(t *testing.T) {
	t.Parallel()
	u := newAmbientTestUI(t)
	u.ambient = newAmbientState(false)
	ev := created("sa", "m1", message.User)
	u.trackTabStatus(ev)
	require.Nil(t, u.trackAmbient(ev))
	require.NotContains(t, ambientRow(u), "─")

	u.ambient = nil
	require.Nil(t, u.trackAmbient(ev))
	require.NotContains(t, ambientRow(u), "─")
}

func TestAmbientEnabledEnv(t *testing.T) {
	off := false
	require.True(t, (*config.TUIOptions)(nil).AmbientEnabled())
	require.False(t, (&config.TUIOptions{Ambient: &off}).AmbientEnabled())

	t.Setenv("TADA_NO_AMBIENT", "1")
	require.False(t, (&config.TUIOptions{}).AmbientEnabled())
	t.Setenv("TADA_NO_AMBIENT", "false")
	require.True(t, (&config.TUIOptions{}).AmbientEnabled())
}
