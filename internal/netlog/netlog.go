// Package netlog keeps a live, in-memory receipt of every host Ta-Da talks
// to over HTTP: which session asked, why, how many requests and how many
// bytes went each way. It is a leaf package so any HTTP client in the
// codebase can wrap its transport without import cycles.
package netlog

import (
	"cmp"
	"context"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Purpose says why Ta-Da made a request.
type Purpose string

const (
	// PurposeProvider is a call to an LLM provider.
	PurposeProvider Purpose = "provider"
	// PurposeMCP is traffic to an MCP server.
	PurposeMCP Purpose = "mcp"
	// PurposeMachine is traffic to another machine in the fleet.
	PurposeMachine Purpose = "machine"
	// PurposeWeb is a web tool: fetch, download, search, Sourcegraph.
	PurposeWeb Purpose = "web"
	// PurposeUpdate is a release check or download.
	PurposeUpdate Purpose = "update"
	// PurposeOAuth is a sign-in or token refresh.
	PurposeOAuth Purpose = "oauth"
	// PurposeCatalog is a model or provider catalog lookup.
	PurposeCatalog Purpose = "catalog"
	// PurposeDiscover is a probe for local model servers.
	PurposeDiscover Purpose = "discover"
	// PurposeEmbed is an embeddings call.
	PurposeEmbed Purpose = "embed"
	// PurposeOther is anything else, such as code using http.DefaultClient.
	PurposeOther Purpose = "other"
)

// Label is the purpose as people read it: "model" rather than "provider".
func (p Purpose) Label() string {
	switch p {
	case PurposeProvider:
		return "model"
	case PurposeOAuth:
		return "sign-in"
	case PurposeCatalog:
		return "model list"
	case PurposeDiscover:
		return "discovery"
	case PurposeEmbed:
		return "embeddings"
	case "":
		return "other"
	default:
		return string(p)
	}
}

// Labels joins the labels of ps with ", ".
func Labels(ps []Purpose) string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Label()
	}
	return strings.Join(out, ", ")
}

// Bytes formats a byte count for people: "0 B", "812 B", "4.1 KB",
// "1.2 MB". Units are powers of 1024.
func Bytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", max(0, n))
	}
	v := float64(n)
	for _, u := range []string{"KB", "MB", "GB"} {
		v /= unit
		if v < unit {
			if v < 10 {
				return fmt.Sprintf("%.1f %s", v, u)
			}
			return fmt.Sprintf("%.0f %s", v, u)
		}
	}
	return fmt.Sprintf("%.1f TB", v/unit)
}

// Host is one host in a report.
type Host struct {
	Host      string    `json:"host"`
	Purposes  []Purpose `json:"purposes"`
	Local     bool      `json:"local"`
	Requests  int64     `json:"requests"`
	BytesOut  int64     `json:"bytes_out"`
	BytesIn   int64     `json:"bytes_in"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// Totals sums a report's hosts.
type Totals struct {
	Hosts    int   `json:"hosts"`
	External int   `json:"external"`
	Requests int64 `json:"requests"`
	BytesOut int64 `json:"bytes_out"`
	BytesIn  int64 `json:"bytes_in"`
}

// Report is the receipt for one session, or for the whole process.
type Report struct {
	// SessionID is the session the report covers, or "" for every
	// request this process made.
	SessionID string `json:"session_id,omitempty"`
	Hosts     []Host `json:"hosts"`
	Totals    Totals `json:"totals"`
	// Background lists, in a session report, the requests Ta-Da made
	// outside any session: update checks, catalogs, sign-ins.
	Background       []Host    `json:"background,omitempty"`
	BackgroundTotals Totals    `json:"background_totals"`
	At               time.Time `json:"at"`
}

type key struct {
	session string
	host    string
	purpose Purpose
}

// entry is one row. Counters are atomic so a streaming body can count
// without taking the ledger lock on every read.
type entry struct {
	requests atomic.Int64
	out      atomic.Int64
	in       atomic.Int64
	first    int64
	last     atomic.Int64
	local    bool
}

func (e *entry) touch(now time.Time) {
	e.last.Store(now.UnixNano())
}

// Ledger records traffic. The zero value is not usable; use NewLedger.
type Ledger struct {
	mu   sync.Mutex
	rows map[key]*entry
	now  func() time.Time
}

// NewLedger returns an empty ledger.
func NewLedger() *Ledger {
	return &Ledger{rows: make(map[key]*entry), now: time.Now}
}

var defaultLedger = NewLedger()

// Default returns the process-wide ledger.
func Default() *Ledger { return defaultLedger }

func (l *Ledger) row(session, host string, p Purpose) *entry {
	host = normalizeHost(host)
	k := key{session: session, host: host, purpose: cmp.Or(p, PurposeOther)}
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.rows[k]
	if !ok {
		now := l.now()
		e = &entry{first: now.UnixNano(), local: IsLocal(host)}
		e.last.Store(now.UnixNano())
		l.rows[k] = e
	}
	return e
}

// Note records that a session reached a host without Ta-Da seeing the
// bytes, for example when the Claude CLI makes its own requests. It counts
// one request and no bytes.
func (l *Ledger) Note(session, host string, p Purpose) {
	if l == nil {
		return
	}
	e := l.row(session, host, p)
	e.requests.Add(1)
	e.touch(l.now())
}

// Add records one request with its byte counts.
func (l *Ledger) Add(session, host string, p Purpose, out, in int64) {
	if l == nil {
		return
	}
	e := l.row(session, host, p)
	e.requests.Add(1)
	e.out.Add(out)
	e.in.Add(in)
	e.touch(l.now())
}

// Reset forgets everything. Tests use it.
func (l *Ledger) Reset() {
	l.mu.Lock()
	l.rows = make(map[key]*entry)
	l.mu.Unlock()
}

// Report returns the receipt for sessionID, or for every request when it
// is "". Hosts are sorted by the most recent activity first.
func (l *Ledger) Report(sessionID string) Report {
	r := Report{SessionID: sessionID, Hosts: []Host{}}
	if l == nil {
		return r
	}
	l.mu.Lock()
	r.At = l.now()
	type agg struct {
		Host
		purposes map[Purpose]struct{}
	}
	mine := map[string]*agg{}
	bg := map[string]*agg{}
	for k, e := range l.rows {
		target := mine
		switch {
		case sessionID == "" || k.session == sessionID:
		case k.session == "":
			target = bg
		default:
			continue
		}
		a, ok := target[k.host]
		if !ok {
			a = &agg{Host: Host{Host: k.host, Local: e.local}, purposes: map[Purpose]struct{}{}}
			target[k.host] = a
		}
		a.purposes[k.purpose] = struct{}{}
		a.Requests += e.requests.Load()
		a.BytesOut += e.out.Load()
		a.BytesIn += e.in.Load()
		first := time.Unix(0, e.first)
		if a.FirstSeen.IsZero() || first.Before(a.FirstSeen) {
			a.FirstSeen = first
		}
		if last := time.Unix(0, e.last.Load()); last.After(a.LastSeen) {
			a.LastSeen = last
		}
	}
	l.mu.Unlock()

	flatten := func(m map[string]*agg) ([]Host, Totals) {
		hosts := make([]Host, 0, len(m))
		var t Totals
		for _, a := range m {
			h := a.Host
			for p := range a.purposes {
				h.Purposes = append(h.Purposes, p)
			}
			slices.Sort(h.Purposes)
			hosts = append(hosts, h)
			t.Hosts++
			if !h.Local {
				t.External++
			}
			t.Requests += h.Requests
			t.BytesOut += h.BytesOut
			t.BytesIn += h.BytesIn
		}
		slices.SortFunc(hosts, func(a, b Host) int {
			if c := b.LastSeen.Compare(a.LastSeen); c != 0 {
				return c
			}
			return strings.Compare(a.Host, b.Host)
		})
		return hosts, t
	}
	r.Hosts, r.Totals = flatten(mine)
	if sessionID != "" {
		r.Background, r.BackgroundTotals = flatten(bg)
		if len(r.Background) == 0 {
			r.Background = nil
		}
	}
	return r
}

// Summary returns just the totals for sessionID, for the status bar.
func (l *Ledger) Summary(sessionID string) Totals {
	return l.Report(sessionID).Totals
}

// normalizeHost lowercases a host and drops a port and IPv6 brackets.
func normalizeHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return "unknown"
	}
	return host
}

// IsLocal reports whether host stays on this machine or its local network:
// loopback, private (RFC 1918 and IPv6 ULA), link-local, localhost and
// .local names.
func IsLocal(host string) bool {
	host = normalizeHost(host)
	if host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		strings.HasSuffix(host, ".local") {
		return true
	}
	if i := strings.IndexByte(host, '%'); i >= 0 {
		// Zoned IPv6, such as fe80::1%en0.
		host = host[:i]
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsUnspecified()
}

type sessionKey struct{}

// WithSession tags ctx with a session for requests made under it.
func WithSession(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, sessionKey{}, sessionID)
}

var sessionFunc atomic.Pointer[func(context.Context) string]

// SetSessionFunc teaches netlog another way to find a request's session.
// The agent's tools package registers its context key this way so netlog
// doesn't have to import it.
func SetSessionFunc(f func(context.Context) string) {
	sessionFunc.Store(&f)
}

// SessionFrom returns the session a request's context belongs to, or "".
func SessionFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if s, ok := ctx.Value(sessionKey{}).(string); ok && s != "" {
		return s
	}
	if f := sessionFunc.Load(); f != nil {
		return (*f)(ctx)
	}
	return ""
}

// HostClaudeCLI stands in for the traffic of the Claude Code CLI, which
// Ta-Da drives as a subprocess. The CLI makes its own connections, so Ta-Da
// can't see the hosts or byte counts; it records one request per turn.
const HostClaudeCLI = "claude cli (own traffic)"
