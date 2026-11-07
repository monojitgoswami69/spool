package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/monojitgoswami69/spool/internal/config"
	"github.com/monojitgoswami69/spool/internal/llm"
	"github.com/monojitgoswami69/spool/internal/ui"
)

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Interactive LLM provider, model, key, and GitHub setup",
	Args:  cobra.NoArgs,
	RunE:  runSetup,
}

func init() {
	rootCmd.AddCommand(setupCmd)
}

func runSetup(cmd *cobra.Command, args []string) error {
	if !isInteractive() {
		return fmt.Errorf("setup needs an interactive terminal; use `spool config set <key> <value>` instead")
	}
	cfg := config.LoadOptional()
	reader := bufio.NewReader(os.Stdin)

	logf("%s", ui.TitleStyle.Render("Spool setup"))
	logf("%s", ui.MutedStyle.Render("Configure the agent, then verify it live."))
	logf("")
	logf("%s", ui.Section("LLM provider"))
	logf("  1) gemini   2) openai   3) anthropic   4) groq   5) ollama")

	provider := config.NormalizeProvider(firstNonEmptyStr(cfg.LLMProvider, "gemini"))
	choice := readLine(reader, fmt.Sprintf("Provider [%s]", provider), "")
	if choice != "" {
		provider = providerFromChoice(choice, provider)
	}

	model := firstNonEmptyStr(cfg.LLMModel, config.DefaultModel(provider))
	model = readLine(reader, "Model", model)
	if model == "" {
		model = config.DefaultModel(provider)
	}

	apiKey := cfg.LLMAPIKey
	if provider != "ollama" {
		label := "API key"
		if apiKey != "" {
			label += " [keep existing]"
		}
		entered := readSecret(label + ": ")
		if entered != "" {
			apiKey = entered
		}
		if strings.TrimSpace(apiKey) == "" {
			return fmt.Errorf("an API key is required for provider %s", provider)
		}
	}

	baseURL := readLine(reader, "Base URL (optional)", cfg.LLMBaseURL)

	pat := cfg.GitHubPAT
	if entered := readSecret("GitHub PAT (optional): "); entered != "" {
		pat = entered
	}

	cfg.LLMProvider = provider
	cfg.LLMModel = model
	cfg.LLMAPIKey = apiKey
	cfg.LLMBaseURL = baseURL
	cfg.GitHubPAT = pat
	if err := config.Save(cfg); err != nil {
		return err
	}
	path, _ := config.ConfigPath()
	logf("%s", ui.SuccessLine("saved "+path))

	client, err := llm.New(llm.Config{
		Provider: provider, Model: model, APIKey: apiKey, BaseURL: baseURL, Timeout: 30 * time.Second,
	})
	if err != nil {
		logf("%s", ui.WarningLine("saved, but the client is invalid: "+err.Error()))
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := client.Call(ctx, "You are a health check.", "Reply with the single word OK.")
	if err != nil {
		logf("%s", ui.WarningLine("saved, but live verification failed: "+err.Error()))
		return nil
	}
	logf("%s", ui.SuccessLine("verified "+provider+"/"+model+" → "+truncateText(strings.TrimSpace(resp), 40)))
	return nil
}

func readLine(r *bufio.Reader, label, def string) string {
	if def != "" {
		fmt.Printf("%s [%s]: ", ui.BoldStyle.Render(label), def)
	} else {
		fmt.Printf("%s: ", ui.BoldStyle.Render(label))
	}
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

func readSecret(label string) string {
	fmt.Print(ui.BoldStyle.Render(label) + " ")
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func providerFromChoice(choice, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(choice)) {
	case "1", "gemini", "google":
		return "gemini"
	case "2", "openai":
		return "openai"
	case "3", "anthropic", "claude":
		return "anthropic"
	case "4", "groq":
		return "groq"
	case "5", "ollama", "local":
		return "ollama"
	default:
		return fallback
	}
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
