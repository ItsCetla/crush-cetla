package agent

import (
	"net/http"

	"github.com/charmbracelet/crush/internal/log"
	"github.com/charmbracelet/crush/internal/netlog"
)

// providerHTTPClient returns the client every LLM provider talks through.
// It logs requests when debug is on and always counts them in the network
// ledger, so the privacy receipt can say which model hosts a session used.
func providerHTTPClient(debug bool) *http.Client {
	return &http.Client{Transport: providerTransport(debug)}
}

// providerTransport is the round tripper behind providerHTTPClient, for
// callers that layer their own transport on top.
func providerTransport(debug bool) http.RoundTripper {
	base := http.DefaultTransport
	if debug {
		base = &log.HTTPRoundTripLogger{Transport: base}
	}
	return netlog.Wrap(base, netlog.PurposeProvider)
}
