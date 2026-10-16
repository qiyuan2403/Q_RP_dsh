// Command qrpdsh runs a harness turn from the command line.
//
// It exists so the composition can be exercised end to end: compose a profile,
// mount it, run one turn, and print the result. Without it the harness is a
// library that nothing proves works together.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/qiyuan2403/Q_RP_dsh/agent"
	"github.com/qiyuan2403/Q_RP_dsh/boot"
	"github.com/qiyuan2403/Q_RP_dsh/llm"
	"github.com/qiyuan2403/Q_RP_dsh/llm/openaichat"
	"github.com/qiyuan2403/Q_RP_dsh/session"
	"github.com/qiyuan2403/Q_RP_dsh/systemprompt"
	"github.com/qiyuan2403/Q_RP_dsh/tools"
)

func main() {
	os.Exit(run())
}

func run() int {
	fs := flag.NewFlagSet("qrpdsh", flag.ContinueOnError)
	var (
		profilePath = fs.String("profile", "", "profile directory to compose (optional)")
		anchor      = fs.String("anchor", "", "installation anchor for bundle resolution (defaults to the executable's directory)")
		home        = fs.String("home", "", "harness home (defaults to ~/.qrpdsh)")
		baseURL     = fs.String("base-url", os.Getenv("Q_RP_DSH_BASE_URL"), "chat-completions API root (env Q_RP_DSH_BASE_URL)")
		apiKey      = fs.String("api-key", os.Getenv("Q_RP_DSH_API_KEY"), "API key (env Q_RP_DSH_API_KEY)")
		model       = fs.String("model", os.Getenv("Q_RP_DSH_MODEL"), "model id (env Q_RP_DSH_MODEL)")
		persona     = fs.String("persona", "", "deployment persona for the system prompt")
		maxSteps    = fs.Int("max-steps", 32, "maximum model calls in one turn")
		timeout     = fs.Duration("timeout", 10*time.Minute, "overall turn timeout")
		showPrompt  = fs.Bool("print-prompt", false, "print the assembled system prompt and exit")
		listProfile = fs.Bool("list-profile", false, "compose the profile, print its rows, and exit")
	)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: qrpdsh [flags] "task"

flags:
`)
		fs.PrintDefaults()
		fmt.Fprint(os.Stderr, `
examples:
  qrpdsh -print-prompt
  qrpdsh -list-profile -profile ./profiles/headless
  qrpdsh -model deepseek-chat -base-url https://api.deepseek.com/v1 -api-key $KEY "say hello"
`)
	}
	if err := fs.Parse(os.Args[1:]); err != nil {
		return 2
	}

	if *showPrompt {
		return printPrompt(*persona)
	}

	if *listProfile {
		return listProfileRows(*profilePath, *anchor)
	}

	task := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if task == "" {
		fs.Usage()
		return 2
	}
	if *baseURL == "" || *model == "" {
		fmt.Fprintln(os.Stderr, "qrpdsh: -base-url and -model are required to run a turn")
		return 2
	}

	return runTurn(runConfig{
		home:     *home,
		profile:  *profilePath,
		anchor:   *anchor,
		baseURL:  *baseURL,
		apiKey:   *apiKey,
		model:    *model,
		persona:  *persona,
		maxSteps: *maxSteps,
		timeout:  *timeout,
		task:     task,
	})
}

type runConfig struct {
	home, profile, anchor string
	baseURL, apiKey       string
	model, persona, task  string
	maxSteps              int
	timeout               time.Duration
}

// resolveHome picks the harness home.
//
// The default is a dot-directory in the user's home, because that is where a
// CLI's durable state belongs and where the user can find it.
func resolveHome(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	base, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("qrpdsh: cannot determine the home directory: %w", err)
	}
	return filepath.Join(base, ".qrpdsh"), nil
}

// buildRegistry returns the tools available to a turn.
//
// The set is deliberately small and honest: the harness core owns the loop, the
// session log, and the provider, and a tool that is not registered here simply
// is not offered to the model. Tools arrive from plugins; until those are
// loaded, the model gets none rather than a placeholder that cannot run.
func buildRegistry() *tools.Registry {
	return tools.NewRegistry()
}

// assemblePrompt builds the system prompt for one deployment.
func assemblePrompt(persona string) (*systemprompt.Assembly, error) {
	r := systemprompt.New()
	if err := r.Section(systemprompt.Section{
		Name:  "harness-identity",
		Order: systemprompt.SectionOrders["HARNESS_IDENTITY"],
		Text:  "You are an agent running on the Q_RP_dsh harness.",
	}); err != nil {
		return nil, err
	}
	if strings.TrimSpace(persona) != "" {
		if err := r.Section(systemprompt.Section{
			Name:  systemprompt.PersonaPrefixSection,
			Order: systemprompt.SectionOrders["DEPLOYMENT_PERSONA_PREFIX"],
			Text:  persona,
		}); err != nil {
			return nil, err
		}
	}
	return r.Assemble(systemprompt.AssembleContext{})
}

func printPrompt(persona string) int {
	assembly, err := assemblePrompt(persona)
	if err != nil {
		fmt.Fprintln(os.Stderr, "qrpdsh:", err)
		return 1
	}
	text, err := systemprompt.Render(assembly)
	if err != nil {
		fmt.Fprintln(os.Stderr, "qrpdsh:", err)
		return 1
	}
	fmt.Println(text)
	return 0
}

// listProfileRows composes a profile and prints its rows, so a composition can
// be inspected without running a model.
func listProfileRows(profilePath, anchor string) int {
	if profilePath == "" {
		fmt.Fprintln(os.Stderr, "qrpdsh: -list-profile requires -profile")
		return 2
	}
	composition, err := boot.LoadProfile(profilePath, anchor, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "qrpdsh:", err)
		return 1
	}
	fmt.Printf("composed %d rows from %d layers\n", len(composition.Entries), len(composition.Layers))
	for _, layer := range composition.Layers {
		fmt.Printf("  layer %-40s %d patches\n", layer.Name, len(layer.Patches))
	}
	for _, w := range composition.Warnings {
		fmt.Printf("  warning: %s\n", w)
	}
	for _, e := range composition.Entries {
		state := ""
		if e.Disabled != nil {
			state = " [disabled]"
		}
		if e.Group {
			state += " [group]"
		}
		fmt.Printf("  %-44s %s%s\n", e.ID, e.Name, state)
	}
	return 0
}

// runTurn opens a session, composes the prompt, and drives one turn.
func runTurn(cfg runConfig) int {
	home, err := resolveHome(cfg.home)
	if err != nil {
		fmt.Fprintln(os.Stderr, "qrpdsh:", err)
		return 1
	}

	// A signal cancels the turn rather than killing the process, so the session
	// log still gets its turn/end bracket.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()

	assembly, err := assemblePrompt(cfg.persona)
	if err != nil {
		fmt.Fprintln(os.Stderr, "qrpdsh:", err)
		return 1
	}
	systemText, err := systemprompt.Render(assembly)
	if err != nil {
		fmt.Fprintln(os.Stderr, "qrpdsh:", err)
		return 1
	}

	cwd, _ := os.Getwd()
	id := session.NewID()
	sessionDir := filepath.Join(home, "sessions", id)
	log, err := session.Create(sessionDir, session.Header{
		ID:        id,
		CreatedAt: time.Now().UnixMilli(),
		CWD:       cwd,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "qrpdsh:", err)
		return 1
	}
	defer log.Close()

	registry := buildRegistry()
	provider := openaichat.New(openaichat.Config{
		BaseURL: cfg.baseURL,
		APIKey:  cfg.apiKey,
	})
	a := agent.New(agent.Config{
		Provider:     provider,
		Registry:     registry,
		Model:        cfg.model,
		SystemPrompt: systemText,
		MaxSteps:     cfg.maxSteps,
	}, log)

	fmt.Fprintf(os.Stderr, "session %s\n", id)
	result, err := a.RunTurn(ctx, cfg.task)
	if err != nil {
		fmt.Fprintln(os.Stderr, "qrpdsh:", err)
		return 1
	}
	if result.Text != "" {
		fmt.Println(result.Text)
	}
	fmt.Fprintf(os.Stderr, "\n[%s after %d step(s)]\n", result.Reason, result.Steps)
	switch result.Reason {
	case agent.StopCompleted:
		return 0
	case agent.StopCancelled:
		return 130
	default:
		if result.Err != nil {
			fmt.Fprintln(os.Stderr, "qrpdsh:", result.Err)
		}
		return 1
	}
}

// ensure the llm import is used even if the provider is swapped out.
var _ llm.Provider = (*openaichat.Client)(nil)

// unused guards against a build where every flag path is trimmed.
var _ = errors.New
