package cmd

import (
	"fmt"
	"io"

	"github.com/charmbracelet/crush/internal/discover"
	"github.com/spf13/cobra"
)

// runDiscoverModels lists the model servers running on this computer and
// how to add each one to tadarc. It doesn't load the config.
func runDiscoverModels(cmd *cobra.Command) error {
	servers := discover.FindLocal(cmd.Context())
	printLocalServers(cmd.OutOrStdout(), servers)
	return nil
}

func printLocalServers(w io.Writer, servers []discover.LocalServer) {
	if len(servers) == 0 {
		fmt.Fprintln(w, "No local model servers found on localhost:11434 (Ollama), :1234 (LM Studio) or :8080 (llama.cpp).")
		return
	}
	for i, s := range servers {
		if i > 0 {
			fmt.Fprintln(w)
		}
		title := s.Name
		if s.Version != "" {
			title += " " + s.Version
		}
		fmt.Fprintf(w, "%s at %s\n", title, s.Root)
		if len(s.Models) == 0 {
			if s.Kind == discover.KindOllama {
				fmt.Fprintln(w, "  No models yet. Download one with: ollama pull qwen2.5-coder")
			} else {
				fmt.Fprintln(w, "  No models loaded yet.")
			}
		}
		for _, m := range s.Models {
			if m.ParameterSize != "" {
				fmt.Fprintf(w, "  %s (%s)\n", m.ID, m.ParameterSize)
			} else {
				fmt.Fprintf(w, "  %s\n", m.ID)
			}
		}
		fmt.Fprintln(w, "  Add to tadarc:")
		fmt.Fprintf(w, "    provider add %s --name %q --type %s --base-url %q\n", s.ProviderID, s.Name, s.ProviderType, s.BaseURL)
	}
}
