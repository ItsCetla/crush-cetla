package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/login"
	"github.com/charmbracelet/crush/internal/logout"
	"github.com/charmbracelet/crush/internal/oauth/copilot"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/spf13/cobra"
)

// loginPlatforms are the platforms offered when `tada login` runs without
// an argument, in the order they are listed. Keep this list in sync with
// the switch in RunE and the logout command.
var loginPlatforms = []struct {
	id   string
	name string
}{
	{id: "copilot", name: "GitHub Copilot"},
	{id: "openai", name: "ChatGPT (OpenAI)"},
	{id: "grok", name: "Grok (xAI)"},
}

var loginCmd = &cobra.Command{
	Aliases: []string{"auth"},
	Use:     "login [platform]",
	Short:   "Login Ta-Da to a platform",
	Long: `Login Ta-Da to a specified platform.
	The platform should be provided as an argument. If no argument is given,
	a list of available platforms will be shown.
	Available platforms are: copilot, openai (chatgpt), grok (xai).`,
	Example: `
	# Choose a platform to authenticate with
	tada login

	# Authenticate with GitHub Copilot
	tada login copilot

	# Authenticate with a ChatGPT (OpenAI) account
	tada login openai

	# Authenticate with a Grok (xAI) account
	tada login grok

	# Force re-authentication even if already logged in
	tada login -f copilot
	`,
	ValidArgs: []cobra.Completion{
		"copilot",
		"github",
		"github-copilot",
		"openai",
		"chatgpt",
		"grok",
		"xai",
	},
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var provider string
		if len(args) > 0 {
			provider = args[0]
		} else {
			var err error
			provider, err = pickLoginPlatform()
			if err != nil {
				return err
			}
			if provider == "" {
				return nil
			}
		}

		// Canonicalize aliases, rejecting unknown platforms before any
		// workspace setup happens.
		switch provider {
		case "copilot", "github", "github-copilot":
			provider = "copilot"
		case "openai", "chatgpt":
			provider = "openai"
		case "grok", "xai":
			provider = "grok"
		default:
			return fmt.Errorf("unknown platform: %s (available platforms: copilot, openai, grok)", provider)
		}

		ws, cleanup, err := setupWorkspaceWithProgressBar(cmd)
		if err != nil {
			return err
		}
		defer cleanup()

		force, _ := cmd.Flags().GetBool("force")
		switch provider {
		case "copilot":
			return loginCopilot(ws, force)
		case "openai":
			return loginOpenAI(ws, force)
		default:
			return loginGrok(ws, force)
		}
	},
}

func init() {
	loginCmd.Flags().BoolP("force", "f", false, "Force re-authentication even if already logged in")
}

// pickLoginPlatform asks which platform to log in to and returns its ID, or
// the empty string when the user cancels.
func pickLoginPlatform() (string, error) {
	names := make([]string, len(loginPlatforms))
	for i, p := range loginPlatforms {
		names[i] = p.name
	}
	choice, err := logout.Choose("Which platform do you want to log in to?", names)
	if err != nil {
		return "", err
	}
	if choice < 0 {
		fmt.Println("Login cancelled.")
		return "", nil
	}
	return loginPlatforms[choice].id, nil
}

func loginCopilot(ws workspace.Workspace, force bool) error {
	if !force {
		cfg := ws.Config()
		if cfg != nil {
			if pc, ok := cfg.Providers.Get("copilot"); ok && pc.OAuthToken != nil {
				fmt.Println("You are already logged in to GitHub Copilot.")
				fmt.Println("Use --force to re-authenticate.")
				return nil
			}
		}
	}

	ctx := getLoginContext()

	if diskToken, hasDiskToken := copilot.RefreshTokenFromDisk(); hasDiskToken {
		fmt.Println("Found existing GitHub Copilot token on disk. Using it to authenticate...")
		token, err := copilot.RefreshToken(ctx, diskToken)
		if err != nil {
			return fmt.Errorf("unable to refresh token from disk: %w", err)
		}
		if err := ws.SetProviderAPIKey(config.ScopeGlobal, "copilot", token); err != nil {
			return err
		}
		fmt.Println()
		fmt.Println("You're now authenticated with GitHub Copilot!")
		return nil
	}

	token, err := login.Run(ctx, login.PlatformCopilot)
	if err != nil {
		if errors.Is(err, copilot.ErrNotAvailable) {
			fmt.Println()
			fmt.Println("GitHub Copilot is unavailable for this account. To signup, go to the following page:")
			fmt.Println()
			lipgloss.Println(lipgloss.NewStyle().Hyperlink(copilot.SignupURL, "id=copilot-signup").Render(copilot.SignupURL))
			fmt.Println()
			fmt.Println("You may be able to request free access if eligible. For more information, see:")
			fmt.Println()
			lipgloss.Println(lipgloss.NewStyle().Hyperlink(copilot.FreeURL, "id=copilot-free").Render(copilot.FreeURL))
		}
		return err
	}

	if err := ws.SetProviderAPIKey(config.ScopeGlobal, "copilot", token); err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("You're now authenticated with GitHub Copilot!")
	return nil
}

func loginOpenAI(ws workspace.Workspace, force bool) error {
	if !force {
		cfg := ws.Config()
		if cfg != nil {
			if pc, ok := cfg.Providers.Get("openai"); ok && pc.OAuthToken != nil {
				fmt.Println("You are already logged in to OpenAI with a ChatGPT account.")
				fmt.Println("Use --force to re-authenticate.")
				return nil
			}
		}
	}

	ctx := getLoginContext()
	token, err := login.Run(ctx, login.PlatformOpenAI)
	if err != nil {
		return err
	}

	if err := ws.SetProviderAPIKey(config.ScopeGlobal, "openai", token); err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("You're now authenticated with your ChatGPT account!")
	return nil
}

func loginGrok(ws workspace.Workspace, force bool) error {
	if !force {
		cfg := ws.Config()
		if cfg != nil {
			if pc, ok := cfg.Providers.Get("xai"); ok && pc.OAuthToken != nil {
				fmt.Println("You are already logged in to xAI with a Grok account.")
				fmt.Println("Use --force to re-authenticate.")
				return nil
			}
		}
	}

	ctx := getLoginContext()
	token, err := login.Run(ctx, login.PlatformGrok)
	if err != nil {
		return err
	}

	if err := ws.SetProviderAPIKey(config.ScopeGlobal, "xai", token); err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("You're now authenticated with your Grok account!")
	return nil
}

func getLoginContext() context.Context {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, os.Kill)
	go func() {
		<-ctx.Done()
		cancel()
	}()
	return ctx
}
