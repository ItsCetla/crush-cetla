package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/charmbracelet/crush/internal/client"
	"github.com/charmbracelet/crush/internal/control"
	"github.com/charmbracelet/crush/internal/netlog"
	"github.com/charmbracelet/crush/internal/server"
	"github.com/spf13/cobra"
)

// errNoNetworkReceipt means no running Ta-Da server could be reached. The
// receipt lives only in the memory of the process that made the requests,
// so there's nothing to read from disk instead.
var errNoNetworkReceipt = errors.New("no running Ta-Da server")

// networkStatus is what "tada status --network --json" prints.
type networkStatus struct {
	Running   bool           `json:"running"`
	Workspace string         `json:"workspace,omitempty"`
	Report    *netlog.Report `json:"report,omitempty"`
}

// runStatusNetwork prints the privacy receipt of a running Ta-Da server.
func runStatusNetwork(cmd *cobra.Command, cwd string, plain, asJSON bool) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
	defer cancel()
	st, err := fetchNetworkStatus(ctx, cwd)
	if err != nil && !errors.Is(err, errNoNetworkReceipt) {
		return err
	}
	w := cmd.OutOrStdout()
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(st)
	}
	if !st.Running {
		writeNoNetwork(w)
		return nil
	}
	writeNetwork(w, *st.Report, time.Now(), plain)
	return nil
}

// fetchNetworkStatus asks a running Ta-Da server for its receipt. It never
// starts a server: a fresh one would have nothing to report.
func fetchNetworkStatus(ctx context.Context, cwd string) (networkStatus, error) {
	var st networkStatus
	hostURL, err := server.ParseHostURL(clientHost)
	if err != nil {
		return st, fmt.Errorf("invalid host URL: %v", err)
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := quickHealthProbe(probeCtx, hostURL); err != nil {
		return st, errNoNetworkReceipt
	}
	c, err := client.NewClient(cwd, hostURL.Scheme, hostURL.Host)
	if err != nil {
		return st, err
	}
	wss, err := c.ListWorkspaces(ctx)
	if err != nil {
		return st, err
	}
	if len(wss) == 0 {
		// A server with no workspace has no control hub to ask.
		return st, errNoNetworkReceipt
	}
	ws := wss[0]
	for _, w := range wss {
		if filepath.Clean(w.Path) == filepath.Clean(cwd) {
			ws = w
			break
		}
	}
	r, err := c.Control(ctx, ws.ID, control.Command{Op: control.OpNetwork})
	if err != nil {
		return st, err
	}
	if r.Network == nil {
		return st, errors.New("the server didn't send a network receipt")
	}
	st.Running = true
	st.Workspace = ws.Path
	st.Report = r.Network
	return st, nil
}

func writeNoNetwork(w io.Writer) {
	fmt.Fprintln(w, "Ta-Da network receipt")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  No running Ta-Da server to ask. The receipt lives only in the memory")
	fmt.Fprintln(w, "  of the Ta-Da that made the requests and is never written to disk.")
	fmt.Fprintln(w, `  Open the cockpit there (ctrl+\ or alt+c, then 5) to see it, or run`)
	fmt.Fprintln(w, "  Ta-Da with TADA_CLIENT_SERVER=1 so this command can reach it.")
}

// writeNetwork prints a receipt. With plain set it uses ASCII only.
func writeNetwork(w io.Writer, r netlog.Report, now time.Time, plain bool) {
	fmt.Fprintln(w, "Ta-Da network receipt")
	fmt.Fprintln(w, "  Every host this Ta-Da server has talked to since it started. Kept in")
	fmt.Fprintln(w, "  memory only, never written to disk; byte counts are estimates.")

	hosts := append(append([]netlog.Host{}, r.Hosts...), r.Background...)
	t := r.Totals
	t.Hosts += r.BackgroundTotals.Hosts
	t.External += r.BackgroundTotals.External
	t.Requests += r.BackgroundTotals.Requests
	t.BytesOut += r.BackgroundTotals.BytesOut
	t.BytesIn += r.BackgroundTotals.BytesIn

	fmt.Fprintln(w)
	switch {
	case t.Hosts == 0:
		fmt.Fprintln(w, "  Nothing has left this machine yet.")
		return
	case t.External == 0:
		fmt.Fprintf(w, "  Local only: %d host%s, all on this machine or network.\n", t.Hosts, plural(t.Hosts))
	default:
		fmt.Fprintf(w, "  %d host%s, %d off this machine.\n", t.Hosts, plural(t.Hosts), t.External)
	}
	fmt.Fprintf(w, "  %d request%s, %s sent, %s received.\n",
		t.Requests, plural(int(t.Requests)), netlog.Bytes(t.BytesOut), netlog.Bytes(t.BytesIn))

	out, local := "↑", "•"
	if plain {
		out, local = "*", "-"
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "    %-32s %-16s %6s %9s %9s  %s\n", "HOST", "FOR", "REQS", "SENT", "RECV", "LAST")
	for _, h := range hosts {
		mark := out
		if h.Local {
			mark = local
		}
		fmt.Fprintf(w, "  %s %-32s %-16s %6d %9s %9s  %s\n", mark, h.Host, netlog.Labels(h.Purposes),
			h.Requests, netlog.Bytes(h.BytesOut), netlog.Bytes(h.BytesIn), control.Ago(h.LastSeen, now))
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  %s left this machine, %s stayed on it or its local network.\n", out, local)
}

// plural returns "s" unless n is 1.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
