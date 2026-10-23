package artifacts

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// Attack tests for sign-in tickets (security pass 2026-10-23).

func signInReq(t *testing.T, h http.Handler, host, ticket string) int {
	req := httptest.NewRequestWithContext(t.Context(), "POST", "/api/signin",
		strings.NewReader(`{"ticket":"`+ticket+`"}`))
	req.Host = host
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tada-Viewer", "1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Code
}

// Many requests redeeming one ticket at the same moment: exactly one wins.
func TestAttackTicketRace(t *testing.T) {
	t.Parallel()
	st, _ := newTestArtifact(t)
	h, err := Handler(st, ServerOptions{}, "http://127.0.0.1:7480")
	require.NoError(t, err)
	ticket := strings.SplitN(st.SignInURL("x"), "#t=", 2)[1]

	var wins atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if signInReq(t, h, "127.0.0.1:7480", ticket) == http.StatusOK {
				wins.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	require.Equal(t, int32(1), wins.Load())
}

// A rebinding page can't redeem a ticket: Host is checked on /api/signin too.
func TestAttackSignInForeignHost(t *testing.T) {
	t.Parallel()
	st, _ := newTestArtifact(t)
	h, err := Handler(st, ServerOptions{}, "http://127.0.0.1:7480")
	require.NoError(t, err)
	ticket := strings.SplitN(st.SignInURL("x"), "#t=", 2)[1]
	require.Equal(t, http.StatusForbidden, signInReq(t, h, "evil.example:7480", ticket))
	// The refused attempt did not burn the ticket.
	require.Equal(t, http.StatusOK, signInReq(t, h, "127.0.0.1:7480", ticket))
}

// Used tickets are recorded in the store, so a second viewer on the same
// store (another port, or a restart within the 2-minute TTL) refuses a
// ticket the first already redeemed.
func TestAttackTicketReplayOtherViewer(t *testing.T) {
	t.Parallel()
	st, _ := newTestArtifact(t)
	a, err := Handler(st, ServerOptions{}, "http://127.0.0.1:7480")
	require.NoError(t, err)
	b, err := Handler(st, ServerOptions{}, "http://127.0.0.1:7481")
	require.NoError(t, err)
	ticket := strings.SplitN(st.SignInURL("x"), "#t=", 2)[1]
	require.Equal(t, http.StatusOK, signInReq(t, a, "127.0.0.1:7480", ticket))
	require.Equal(t, http.StatusUnauthorized, signInReq(t, a, "127.0.0.1:7480", ticket))
	require.Equal(t, http.StatusUnauthorized, signInReq(t, b, "127.0.0.1:7481", ticket), "a ticket used on one viewer must not work on another")
}
