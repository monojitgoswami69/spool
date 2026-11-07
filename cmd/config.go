package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monojitgoswami69/spool/internal/config"
	"github.com/monojitgoswami69/spool/internal/ui"
)

var configCmd = &cobra.Command{
	Use:   "config [show|path|edit|set <key> <value>]",
	Short: "Open the interactive config panel, or show/set configuration",
	Args:  cobra.ArbitraryArgs,
	RunE:  runConfig,
}

func init() {
	rootCmd.AddCommand(configCmd)
}

func runConfig(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		if isInteractive() {
			return runConfigPanel()
		}
		return showConfig()
	}
	action := args[0]
	switch action {
	case "show":
		return showConfig()
	case "path":
		p, err := config.ConfigPath()
		if err != nil {
			return err
		}
		fmt.Println(p)
		return nil
	case "edit":
		return runConfigPanel()
	case "set":
		if len(args) < 3 {
			if isInteractive() {
				return runConfigPanel()
			}
			return fmt.Errorf("usage: spool config set <provider|model|api-key|base-url|github-pat> <value>")
		}
		return setConfig(args[1], strings.Join(args[2:], " "))
	default:
		return fmt.Errorf("unknown config action %q (expected show, path, edit, or set)", action)
	}
}

func showConfig() error {
	cfg := config.LoadOptional()
	resolved, rerr := config.Resolve()

	provider, model, base, source := cfg.LLMProvider, cfg.LLMModel, cfg.LLMBaseURL, "config.json"
	keyState := "missing"
	if resolved != nil {
		provider, model, base, source = resolved.Provider, resolved.Model, resolved.BaseURL, resolved.Source
		if resolved.APIKey != "" {
			keyState = "configured (" + resolved.Source + ")"
		}
	}
	if rerr != nil {
		keyState = "not configured"
	}
	if source == "" {
		source = "config.json"
	}
	if base == "" {
		base = "(provider default)"
	}

	home, _ := config.Home()
	cfgPath, _ := config.ConfigPath()
	repos, _ := config.ReposDir()
	projects, _ := config.ProjectsDir()

	configuredProvider, configuredModel := cfg.LLMProvider, cfg.LLMModel
	if configuredProvider == "" {
		configuredProvider = "(not set)"
	}
	if configuredModel == "" {
		configuredModel = "(not set)"
	}
	rows := [][]string{
		{"provider (resolved)", provider},
		{"model (resolved)", model},
		{"provider (config)", configuredProvider},
		{"model (config)", configuredModel},
		{"api key", keyState},
		{"base url", base},
		{"resolved from", source},
		{"config file", cfgPath},
		{"spool home", home},
		{"repos", repos},
		{"projects", projects},
		{"dotenv files", strings.Join(config.DotEnvFiles(), ", ")},
	}
	logf("%s", ui.Table([]string{"Setting", "Value"}, rows, "configuration"))
	if rerr != nil {
		logf("%s", ui.WarningLine("LLM not configured: "+rerr.Error()))
		logf("%s", ui.InfoLine("set a key with: spool config set api-key <value>  (or export GEMINI_API_KEY)"))
	}
	return nil
}

func setConfig(key, value string) error {
	cfg := config.LoadOptional()
	switch strings.ToLower(key) {
	case "provider":
		cfg.LLMProvider = value
	case "model":
		cfg.LLMModel = value
	case "api-key", "api_key", "apikey":
		cfg.LLMAPIKey = value
	case "base-url", "base_url", "baseurl":
		cfg.LLMBaseURL = value
	case "github-pat", "github_pat", "pat":
		cfg.GitHubPAT = value
	default:
		return fmt.Errorf("unknown config key %q (provider, model, api-key, base-url, github-pat)", key)
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	display := value
	if strings.Contains(strings.ToLower(key), "key") || strings.Contains(strings.ToLower(key), "pat") {
		display = "••••••••"
	}
	logf("%s", ui.SuccessLine("set "+key+"="+display))
	if r, rerr := config.Resolve(); rerr == nil {
		switch strings.ToLower(key) {
		case "provider":
			if config.NormalizeProvider(r.Provider) != config.NormalizeProvider(value) {
				logf("%s", ui.WarningLine("an env/.env value is overriding provider (resolved: "+r.Provider+")"))
			}
		case "model":
			if r.Model != value {
				logf("%s", ui.WarningLine("an env/.env value is overriding model (resolved: "+r.Model+")"))
			}
		case "api-key", "api_key", "apikey":
			if r.APIKey != value {
				logf("%s", ui.WarningLine("an env/.env key is overriding the configured API key"))
			}
		}
	}
	logf("%s", ui.MutedStyle.Render("verify with: spool doctor"))
	return nil
}
