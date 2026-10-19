package intro

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/ui/logo"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/stretchr/testify/require"
)

// fakeInner records the messages it receives.
type fakeInner struct{ got []tea.Msg }

func (f *fakeInner) Init() tea.Cmd { return nil }

func (f *fakeInner) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	f.got = append(f.got, msg)
	return f, nil
}

func (f *fakeInner) View() tea.View { return tea.NewView("inner") }

type otherMsg struct{}

func newTest(t *testing.T) (*Model, *fakeInner, *time.Time) {
	t.Helper()
	inner := &fakeInner{}
	st := styles.Default()
	m := New(inner, Options{Styles: &st, Maker: "cetla", Version: "dev", Subtitle: "~/proj"})
	clock := time.Unix(1_000, 0)
	m.now = func() time.Time { return clock }
	m.Init()
	return m, inner, &clock
}

func TestKeySkipsAndIsConsumed(t *testing.T) {
	t.Parallel()
	m, inner, _ := newTest(t)
	require.True(t, m.Active())

	m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	require.False(t, m.Active())
	require.Empty(t, inner.got, "the skip key must not reach the UI")

	m.Update(tea.KeyPressMsg{Code: 'b', Text: "b"})
	require.Len(t, inner.got, 1, "keys after the intro reach the UI")
}

func TestForwardsWhileActive(t *testing.T) {
	t.Parallel()
	m, inner, _ := newTest(t)

	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.Update(otherMsg{})
	require.True(t, m.Active())
	require.Len(t, inner.got, 2)
	require.Equal(t, 120, m.width)

	m.Update(tea.KeyReleaseMsg{Code: 'a'})
	require.Len(t, inner.got, 2, "key releases are dropped during the intro")
}

func TestEndsAfterDuration(t *testing.T) {
	t.Parallel()
	m, inner, clock := newTest(t)

	_, cmd := m.Update(tickMsg{})
	require.True(t, m.Active())
	require.NotNil(t, cmd, "keeps ticking while playing")

	*clock = clock.Add(Duration)
	_, cmd = m.Update(tickMsg{})
	require.False(t, m.Active())
	require.Nil(t, cmd)

	m.Update(tickMsg{})
	require.Empty(t, inner.got, "intro ticks never reach the UI")
	require.Equal(t, "inner", m.View().Content)
}

func TestViewWhileActive(t *testing.T) {
	t.Parallel()
	m, _, clock := newTest(t)
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	*clock = clock.Add(Duration - time.Millisecond)

	v := m.View()
	require.True(t, v.AltScreen)
	require.NotNil(t, v.BackgroundColor)
	require.Contains(t, v.Content, "█")
	require.Contains(t, v.Content, "cetla")
	require.Contains(t, v.Content, "~/proj")
}

func TestFrameSizes(t *testing.T) {
	t.Parallel()
	sizes := [][2]int{{0, 0}, {39, 24}, {40, 8}, {41, 9}, {80, 24}, {300, 80}}
	for _, sz := range sizes {
		m, _, _ := newTest(t)
		m.Update(tea.WindowSizeMsg{Width: sz[0], Height: sz[1]})
		for _, at := range []float64{0, 0.1, 0.5, 1.0, 1.7, 5} {
			out := m.Frame(at)
			if sz[0] < minWidth || sz[1] < minHeight {
				require.Empty(t, out)
				continue
			}
			lines := strings.Split(out, "\n")
			require.Len(t, lines, sz[1], "size %v at %.1fs", sz, at)
		}
	}
}

func TestWordmarkBitmap(t *testing.T) {
	t.Parallel()
	bm := wordmarkBitmap("▀▄█ \n █▀▄")
	require.Len(t, bm, 4)
	require.Len(t, bm[0], 4)
	require.Equal(t, []bool{true, false, true, false}, bm[0])
	require.Equal(t, []bool{false, true, true, false}, bm[1])
	require.Equal(t, []bool{false, true, true, false}, bm[2])
	require.Equal(t, []bool{false, true, false, true}, bm[3])

	full := wordmarkBitmap(logo.Wordmark())
	require.Len(t, full, 6)
	require.Len(t, full[0], 23)
}
