// Package main is the entry point for the Crush CLI.
package main

import (
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"

	"github.com/charmbracelet/crush/internal/cmd"
)

func main() {
	if os.Getenv("CRUSH_PROFILE") != "" {
		go servePprof()
	}

	cmd.Execute()
}

// servePprof serves the pprof handlers on localhost from a dedicated mux.
// Importing net/http/pprof still registers them on http.DefaultServeMux in
// its init, so nothing in crush may ever serve DefaultServeMux.
func servePprof() {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	slog.Info("Serving pprof at localhost:6060")
	if err := http.ListenAndServe("localhost:6060", mux); err != nil {
		slog.Error("Failed to pprof listen", "error", err)
	}
}
