package netlog

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIsLocal(t *testing.T) {
	t.Parallel()
	for host, want := range map[string]bool{
		"localhost":          true,
		"LOCALHOST:8080":     true,
		"app.localhost":      true,
		"printer.local":      true,
		"127.0.0.1":          true,
		"127.0.0.1:11434":    true,
		"::1":                true,
		"[::1]:443":          true,
		"10.0.0.7":           true,
		"172.16.4.2":         true,
		"192.168.1.20":       true,
		"169.254.10.1":       true,
		"fe80::1%en0":        true,
		"fd00::5":            true,
		"0.0.0.0":            true,
		"::ffff:192.168.1.1": true,
		"api.anthropic.com":  false,
		"8.8.8.8":            false,
		"172.32.0.1":         false,
		"2606:4700::1111":    false,
		"local.example.com":  false,
		"":                   false,
	} {
		require.Equal(t, want, IsLocal(host), host)
	}
}

func TestLedgerReport(t *testing.T) {
	t.Parallel()
	l := NewLedger()
	clock := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return clock }

	l.Add("s1", "api.anthropic.com:443", PurposeProvider, 100, 2000)
	clock = clock.Add(time.Minute)
	l.Add("s1", "API.anthropic.com", PurposeProvider, 50, 500)
	l.Add("s1", "127.0.0.1:8080", PurposeMCP, 10, 20)
	l.Add("s2", "duckduckgo.com", PurposeWeb, 5, 5)
	clock = clock.Add(time.Minute)
	l.Add("", "api.github.com", PurposeUpdate, 1, 1)
	l.Note("s1", "api.anthropic.com", PurposeOAuth)

	r := l.Report("s1")
	require.Equal(t, "s1", r.SessionID)
	require.Equal(t, Totals{Hosts: 2, External: 1, Requests: 4, BytesOut: 160, BytesIn: 2520}, r.Totals)
	require.Len(t, r.Hosts, 2)
	a := r.Hosts[0]
	require.Equal(t, "api.anthropic.com", a.Host)
	require.Equal(t, []Purpose{PurposeOAuth, PurposeProvider}, a.Purposes)
	require.False(t, a.Local)
	require.Equal(t, int64(3), a.Requests)
	require.Equal(t, clock.Add(-2*time.Minute), a.FirstSeen.UTC())
	require.Equal(t, clock, a.LastSeen.UTC())
	require.Equal(t, "127.0.0.1", r.Hosts[1].Host)
	require.True(t, r.Hosts[1].Local)

	require.Len(t, r.Background, 1)
	require.Equal(t, "api.github.com", r.Background[0].Host)
	require.Equal(t, 1, r.BackgroundTotals.External)

	all := l.Report("")
	require.Equal(t, 4, all.Totals.Hosts)
	require.Nil(t, all.Background)

	require.Equal(t, Totals{}, l.Report("nobody").Totals)
	require.Equal(t, 1, l.Summary("s2").External)

	var nilLedger *Ledger
	require.Empty(t, nilLedger.Report("s1").Hosts)
	nilLedger.Add("s1", "x", PurposeOther, 1, 1)
}

func TestTransportCountsAndAttributes(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Echo", "1")
		_, _ = w.Write([]byte(strings.Repeat("y", 1000)))
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	l := NewLedger()
	c := &http.Client{Transport: &Transport{Purpose: PurposeProvider, Ledger: l}}
	ctx := WithSession(t.Context(), "sess-1")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/v1/messages", strings.NewReader(strings.Repeat("x", 300)))
	require.NoError(t, err)
	resp, err := c.Do(req)
	require.NoError(t, err)
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Len(t, got, 1300)

	r := l.Report("sess-1")
	require.Len(t, r.Hosts, 1)
	h := r.Hosts[0]
	require.Equal(t, "127.0.0.1", h.Host)
	require.True(t, h.Local)
	require.Equal(t, []Purpose{PurposeProvider}, h.Purposes)
	require.Equal(t, int64(1), h.Requests)
	// Bodies are exact; headers add an estimate on top.
	require.Greater(t, h.BytesOut, int64(300))
	require.Less(t, h.BytesOut, int64(600))
	require.Greater(t, h.BytesIn, int64(1300))
	require.Less(t, h.BytesIn, int64(1600))

	// A request without a session lands in the background.
	req, err = http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	resp, err = c.Do(req)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	require.Equal(t, int64(1), l.Report("sess-1").Totals.Requests)
	require.Equal(t, 1, l.Report("sess-1").BackgroundTotals.Hosts)
}

func TestTransportStreams(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("data: one\n\n"))
		w.(http.Flusher).Flush()
		<-release
		_, _ = w.Write([]byte("data: two\n\n"))
	}))
	t.Cleanup(srv.Close)

	l := NewLedger()
	c := &http.Client{Transport: &Transport{Purpose: PurposeProvider, Ledger: l}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	resp, err := c.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// The first event arrives before the server finishes: nothing buffers.
	buf := make([]byte, 64)
	n, err := resp.Body.Read(buf)
	require.NoError(t, err)
	require.Equal(t, "data: one\n\n", string(buf[:n]))
	before := l.Report("").Totals.BytesIn
	close(release)
	rest, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "data: two\n\n", string(rest))
	require.Equal(t, before+int64(len(rest)), l.Report("").Totals.BytesIn)
}

func TestSessionFunc(t *testing.T) {
	t.Parallel()
	type otherKey struct{}
	ctx := context.WithValue(t.Context(), otherKey{}, "from-func")
	SetSessionFunc(func(ctx context.Context) string {
		s, _ := ctx.Value(otherKey{}).(string)
		return s
	})
	require.Equal(t, "from-func", SessionFrom(ctx))
	require.Equal(t, "explicit", SessionFrom(WithSession(ctx, "explicit")))
	require.Empty(t, SessionFrom(t.Context()))
}

func TestWrapDoesNotDoubleCount(t *testing.T) {
	t.Parallel()
	inner := Wrap(http.DefaultTransport, PurposeOther)
	outer := Wrap(inner, PurposeMCP)
	tr, ok := outer.(*Transport)
	require.True(t, ok)
	require.Equal(t, PurposeMCP, tr.Purpose)
	require.Equal(t, http.DefaultTransport, tr.Base)

	c := Client(nil, PurposeWeb)
	require.Equal(t, PurposeWeb, c.Transport.(*Transport).Purpose)
}

type rwcBody struct {
	io.Reader
	written []byte
}

func (b *rwcBody) Write(p []byte) (int, error) {
	b.written = append(b.written, p...)
	return len(p), nil
}

func (b *rwcBody) Close() error { return nil }

type fixedRT struct{ resp *http.Response }

func (f fixedRT) RoundTrip(*http.Request) (*http.Response, error) { return f.resp, nil }

func TestTransportKeepsUpgradedBodyWritable(t *testing.T) {
	t.Parallel()
	body := &rwcBody{Reader: strings.NewReader("hello")}
	l := NewLedger()
	tr := &Transport{
		Base:    fixedRT{resp: &http.Response{StatusCode: http.StatusSwitchingProtocols, Body: body, Header: http.Header{}}},
		Purpose: PurposeProvider,
		Ledger:  l,
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "https://api.openai.com/v1/responses", nil)
	resp, err := tr.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	rwc, ok := resp.Body.(io.ReadWriteCloser)
	require.True(t, ok, "upgraded bodies must stay writable")
	_, err = rwc.Write([]byte("abc"))
	require.NoError(t, err)
	require.Equal(t, "abc", string(body.written))
	got, err := io.ReadAll(rwc)
	require.NoError(t, err)
	require.Equal(t, "hello", string(got))

	h := l.Report("").Hosts[0]
	require.Equal(t, "api.openai.com", h.Host)
	require.False(t, h.Local)
	require.GreaterOrEqual(t, h.BytesIn, int64(5))
	require.GreaterOrEqual(t, h.BytesOut, int64(3))
}

func TestBytes(t *testing.T) {
	t.Parallel()
	cases := map[int64]string{
		-1:                     "0 B",
		0:                      "0 B",
		812:                    "812 B",
		1024:                   "1.0 KB",
		4200:                   "4.1 KB",
		30 * 1024:              "30 KB",
		1258291:                "1.2 MB",
		5 * 1024 * 1024 * 1024: "5.0 GB",
	}
	for n, want := range cases {
		require.Equal(t, want, Bytes(n), "Bytes(%d)", n)
	}
}

func TestLabels(t *testing.T) {
	t.Parallel()
	require.Equal(t, "model, mcp, sign-in", Labels([]Purpose{PurposeProvider, PurposeMCP, PurposeOAuth}))
	require.Equal(t, "", Labels(nil))
}
