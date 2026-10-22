package tools

import (
	"net/http"
	"time"

	"github.com/charmbracelet/crush/internal/netlog"
)

func init() {
	// Let the network receipt attribute requests to the session whose
	// turn made them.
	netlog.SetSessionFunc(GetSessionFromContext)
}

// NewWebClient returns the HTTP client the web tools use when they aren't
// given one. Its requests show up on the network receipt as web traffic.
func NewWebClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 10
	transport.IdleConnTimeout = 90 * time.Second
	return &http.Client{
		Timeout:   timeout,
		Transport: netlog.Wrap(transport, netlog.PurposeWeb),
	}
}
