package cmd

import (
	"fmt"
	"log/slog"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/x/exp/charmtone"
	"github.com/spf13/cobra"
)

var updateProvidersSource string

var updateProvidersCmd = &cobra.Command{
	Use:   "update-providers [path-or-url]",
	Short: "Update providers",
	Long: `Update provider information from a specified local path or remote URL.

Ta-Da never updates the provider catalog on its own: it uses the catalog
saved by this command, or the one embedded in this release when none has
been saved. Running this command is the only way to fetch a newer catalog.`,
	Example: `
# Update Catwalk providers remotely (default)
tada update-providers

# Update Catwalk providers from a custom URL
tada update-providers https://example.com/providers.json

# Update Catwalk providers from a local file
tada update-providers /path/to/local-providers.json

# Update Catwalk providers from embedded version
tada update-providers embedded
`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// NOTE(@andreynering): We want to skip logging output do stdout here.
		slog.SetDefault(slog.New(slog.DiscardHandler))

		var pathOrURL string
		if len(args) > 0 {
			pathOrURL = args[0]
		}

		if updateProvidersSource != "catwalk" {
			return fmt.Errorf("invalid source %q, the only supported source is 'catwalk'", updateProvidersSource)
		}
		if err := config.UpdateProviders(pathOrURL); err != nil {
			return err
		}

		// NOTE(@andreynering): This style is more-or-less copied from Fang's
		// error message, adapted for success.
		headerStyle := lipgloss.NewStyle().
			Foreground(charmtone.Butter).
			Background(charmtone.Guac).
			Bold(true).
			Padding(0, 1).
			Margin(1).
			MarginLeft(2).
			SetString("SUCCESS")
		textStyle := lipgloss.NewStyle().
			MarginLeft(2).
			SetString(fmt.Sprintf("%s provider updated successfully.", updateProvidersSource))

		fmt.Printf("%s\n%s\n\n", headerStyle.Render(), textStyle.Render())
		return nil
	},
}

func init() {
	updateProvidersCmd.Flags().StringVar(&updateProvidersSource, "source", "catwalk", "Provider source to update (only catwalk is supported)")
	_ = updateProvidersCmd.Flags().MarkDeprecated("source", "catwalk is the only provider source")
}
