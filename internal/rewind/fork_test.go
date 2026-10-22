package rewind

import (
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// forkFixture is a session with three turns, a summary in the second and a
// file edited in every turn.
type forkFixture struct {
	sessID                      string
	path                        string
	u1, a1, u2, sum, a2, u3, a3 message.Message
}

func newForkFixture(t *testing.T, e *env) forkFixture {
	t.Helper()
	s := e.session(t, "Parser work")
	wd := t.TempDir()
	_, err := e.sessions.SetWorkingDir(e.ctx, s.ID, wd)
	require.NoError(t, err)
	s.Cost, s.PromptTokens = 3.5, 1200
	_, err = e.sessions.Save(e.ctx, s)
	require.NoError(t, err)

	f := forkFixture{sessID: s.ID, path: filepath.Join(wd, "main.go")}
	f.u1 = e.msg(t, s.ID, message.User, 100, user("first"))
	f.a1 = e.msg(t, s.ID, message.Assistant, 101, message.TextContent{Text: "ok"})
	e.at(t, 101)(e.history.CreateNew(e.ctx, s.ID, f.path))
	e.at(t, 101)(e.history.CreateVersion(e.ctx, s.ID, f.path, "one"))
	require.NoError(t, e.q.RecordFileRead(e.ctx, db.RecordFileReadParams{SessionID: s.ID, Path: "early.go"}))
	_, err = e.conn.ExecContext(e.ctx, `UPDATE read_files SET read_at = 102 WHERE path = 'early.go'`)
	require.NoError(t, err)

	f.u2 = e.msg(t, s.ID, message.User, 200, user("second"))
	f.sum = e.summary(t, s.ID, 201)
	f.a2 = e.msg(t, s.ID, message.Assistant, 202, message.TextContent{Text: "done"})
	e.at(t, 202)(e.history.CreateVersion(e.ctx, s.ID, f.path, "two"))

	f.u3 = e.msg(t, s.ID, message.User, 300, user("third"))
	f.a3 = e.msg(t, s.ID, message.Assistant, 301, message.TextContent{Text: "again"})
	e.at(t, 301)(e.history.CreateVersion(e.ctx, s.ID, f.path, "three"))
	require.NoError(t, e.q.RecordFileRead(e.ctx, db.RecordFileReadParams{SessionID: s.ID, Path: "late.go"}))
	_, err = e.conn.ExecContext(e.ctx, `UPDATE read_files SET read_at = 302 WHERE path = 'late.go'`)
	require.NoError(t, err)
	return f
}

func (e *env) texts(t *testing.T, sessionID string) []string {
	t.Helper()
	msgs, err := e.messages.List(e.ctx, sessionID)
	require.NoError(t, err)
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Content().Text)
	}
	return out
}

func (e *env) versions(t *testing.T, sessionID string) []string {
	t.Helper()
	files, err := e.history.ListBySession(e.ctx, sessionID)
	require.NoError(t, err)
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Content)
	}
	return out
}

func (e *env) reads(t *testing.T, sessionID string) []string {
	t.Helper()
	rows, err := e.q.ListSessionReadFiles(e.ctx, sessionID)
	require.NoError(t, err)
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Path)
	}
	return out
}

func TestForkWholeSession(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	f := newForkFixture(t, e)

	fork, err := e.svc.Fork(e.ctx, f.sessID, "")
	require.NoError(t, err)
	require.NotEqual(t, f.sessID, fork.ID)
	require.Equal(t, "Fork of Parser work", fork.Title)
	require.Empty(t, fork.ParentSessionID)
	require.Zero(t, fork.Cost)
	require.Zero(t, fork.PromptTokens)

	src, err := e.sessions.Get(e.ctx, f.sessID)
	require.NoError(t, err)
	require.Equal(t, src.WorkingDir, fork.WorkingDir)
	require.Equal(t, e.texts(t, f.sessID), e.texts(t, fork.ID))
	require.EqualValues(t, 7, fork.MessageCount)
	require.Equal(t, []string{"", "one", "two", "three"}, e.versions(t, fork.ID))
	require.ElementsMatch(t, []string{"early.go", "late.go"}, e.reads(t, fork.ID))

	// The summary pointer moves to the copy.
	require.NotEmpty(t, fork.SummaryMessageID)
	require.NotEqual(t, f.sum.ID, fork.SummaryMessageID)
	sum, err := e.messages.Get(e.ctx, fork.SummaryMessageID)
	require.NoError(t, err)
	require.Equal(t, fork.ID, sum.SessionID)
	require.True(t, sum.IsSummaryMessage)

	// The original is untouched.
	require.Len(t, e.texts(t, f.sessID), 7)
	require.Len(t, e.versions(t, f.sessID), 4)
}

func TestForkBeforeUserMessage(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	f := newForkFixture(t, e)

	fork, err := e.svc.Fork(e.ctx, f.sessID, f.u2.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"first", "ok"}, e.texts(t, fork.ID))
	require.Equal(t, []string{"", "one"}, e.versions(t, fork.ID))
	require.Equal(t, []string{"early.go"}, e.reads(t, fork.ID))
	require.Empty(t, fork.SummaryMessageID, "the summary came after the cut")

	// The fork rewinds like the original: its first turn deletes the file
	// the agent created.
	write(t, f.path, "one")
	res, err := e.svc.Rewind(e.ctx, fork.ID, Request{MessageID: e.firstID(t, fork.ID), Mode: ModeFiles, DryRun: true})
	require.NoError(t, err)
	require.Equal(t, []FileChange{{Path: f.path, Op: OpDelete}}, res.Files)
}

func TestForkAfterAssistantTurn(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	f := newForkFixture(t, e)

	// Any message of the second turn copies through its end.
	for _, at := range []string{f.sum.ID, f.a2.ID} {
		fork, err := e.svc.Fork(e.ctx, f.sessID, at)
		require.NoError(t, err)
		require.Equal(t, []string{"first", "ok", "second", "summary", "done"}, e.texts(t, fork.ID))
		require.Equal(t, []string{"", "one", "two"}, e.versions(t, fork.ID))
		require.NotEmpty(t, fork.SummaryMessageID)
	}

	// The last turn's reply copies everything.
	fork, err := e.svc.Fork(e.ctx, f.sessID, f.a3.ID)
	require.NoError(t, err)
	require.Len(t, e.texts(t, fork.ID), 7)
}

func TestForkErrors(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	f := newForkFixture(t, e)

	_, err := e.svc.Fork(e.ctx, f.sessID, "nope")
	require.ErrorIs(t, err, ErrNotFound)

	// While the agent works only finished turns can be copied.
	e.busy[f.sessID] = true
	_, err = e.svc.Fork(e.ctx, f.sessID, "")
	require.ErrorIs(t, err, ErrBusy)
	_, err = e.svc.Fork(e.ctx, f.sessID, f.u3.ID)
	require.NoError(t, err)
}

func TestForkTitle(t *testing.T) {
	t.Parallel()
	require.Equal(t, "Fork of x", forkTitle(" x "))
	require.Equal(t, "Fork of x", forkTitle("Fork of x"))
	require.Equal(t, "Fork of untitled session", forkTitle(""))
}

func (e *env) firstID(t *testing.T, sessionID string) string {
	t.Helper()
	msgs, err := e.messages.List(e.ctx, sessionID)
	require.NoError(t, err)
	require.NotEmpty(t, msgs)
	return msgs[0].ID
}
