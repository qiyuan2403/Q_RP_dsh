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
	"github.com/qiyuan2403/Q_RP_dsh/runtime"
	"github.com/qiyuan2403/Q_RP_dsh/session"
	"github.com/qiyuan2403/Q_RP_dsh/systemprompt"
	"github.com/qiyuan2403/Q_RP_dsh/tools"
	builtintools "github.com/qiyuan2403/Q_RP_dsh/tools/builtin"
	toolweb "github.com/qiyuan2403/Q_RP_dsh/tools/web"
	"github.com/qiyuan2403/Q_RP_dsh/web"
	"github.com/qiyuan2403/Q_RP_dsh/web/deepseeksearch"
	"github.com/qiyuan2403/Q_RP_dsh/web/httpfetch"
	"github.com/qiyuan2403/Y_Cordis/jsbridge"
	"github.com/qiyuan2403/Y_Cordis/workspace"
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
		workDir     = fs.String("workdir", "", "working directory the file tools are bounded by (defaults to the process's)")
		allowShell  = fs.Bool("allow-shell", false, "offer the bash tool (runs commands in the working directory)")
		timeout     = fs.Duration("timeout", 10*time.Minute, "overall turn timeout")
		showPrompt  = fs.Bool("print-prompt", false, "print the assembled system prompt and exit")
		listProfile = fs.Bool("list-profile", false, "compose the profile, print its rows, and exit")
		mountOnly   = fs.Bool("mount", false, "compose and mount the profile onto the kernel, then exit")
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
  qrpdsh -mount -profile ./profiles/headless -anchor ./package.json
  qrpdsh -model deepseek-chat -base-url https://api.deepseek.com/v1 -api-key $KEY "say hello"
`)
	}
	if err := fs.Parse(os.Args[1:]); err != nil {
		return 2
	}

	if *showPrompt {
		return printPrompt(runConfig{
			home:       *home,
			persona:    *persona,
			workDir:    *workDir,
			allowShell: *allowShell,
		})
	}

	if *listProfile {
		return listProfileRows(*profilePath, *anchor)
	}

	if *mountOnly {
		return mountProfile(*profilePath, *anchor, *home)
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
		home:       *home,
		profile:    *profilePath,
		anchor:     *anchor,
		baseURL:    *baseURL,
		apiKey:     *apiKey,
		model:      *model,
		persona:    *persona,
		maxSteps:   *maxSteps,
		timeout:    *timeout,
		workDir:    *workDir,
		allowShell: *allowShell,
		task:       task,
	})
}

type runConfig struct {
	home, profile, anchor string
	baseURL, apiKey       string
	model, persona, task  string
	workDir               string
	maxSteps              int
	timeout               time.Duration
	allowShell            bool
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

// buildToolset returns the tools available to a turn and the web service the
// web tools call.
//
// The web service is returned rather than kept private because registering its
// providers is the deployment's decision: providers need credentials and
// endpoints the registry does not own, and a missing provider must surface as a
// structured tool error rather than a missing tool.
//
// Every tool is bounded by `root`, so a model mistake cannot reach outside the
// directory the harness was pointed at. The plugin profile is the only other
// directory the built-in tools are allowed to manage.
func buildToolset(root, profileDir string, allowShell bool) (*tools.Registry, *web.Web, error) {
	r := tools.NewRegistry()
	if err := builtintools.Register(r, builtintools.Options{
		Root:       root,
		AllowShell: allowShell,
		ProfileDir: profileDir,
	}); err != nil {
		return nil, nil, err
	}
	w := web.New(web.Config{})
	if err := registerWebProviders(w); err != nil {
		return nil, nil, err
	}
	if err := toolweb.Register(r, w, toolweb.Options{}); err != nil {
		return nil, nil, err
	}
	return r, w, nil
}

// registerWebProviders mounts the web providers this deployment ships.
//
// Both are registered even when one is unusable: the DeepSeek search provider
// reports unavailable until DEEPSEEK_API_KEY exists, and the seam turns that
// into a structured selection error at call time rather than hiding the
// capability. Provider selection itself stays on the web service, so a
// deployment that wants a different provider only changes this function.
func registerWebProviders(w *web.Web) error {
	if w == nil {
		return nil
	}
	fetch, err := httpfetch.New(httpfetch.Config{})
	if err != nil {
		return fmt.Errorf("qrpdsh: constructing the http fetch provider: %w", err)
	}
	if _, err := w.RegisterFetchProvider(fetch); err != nil {
		return fmt.Errorf("qrpdsh: registering the http fetch provider: %w", err)
	}
	search, err := deepseeksearch.New(deepseeksearch.Config{})
	if err != nil {
		return fmt.Errorf("qrpdsh: constructing the deepseek search provider: %w", err)
	}
	if _, err := w.RegisterSearchProvider(search); err != nil {
		return fmt.Errorf("qrpdsh: registering the deepseek search provider: %w", err)
	}
	return nil
}

// assemblePrompt builds the system prompt for one deployment.
//
// Tool-owned guidance is contributed from the registered tools, not from a
// central block: the text that describes a tool rides with the tool that
// carries the behavior, which is how the official harness keeps the two from
// drifting. A nil registry means "no tools to describe" and leaves the prompt
// with only its identity and persona sections.
func assemblePrompt(persona string, registry *tools.Registry) (*systemprompt.Assembly, error) {
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
	if registry != nil {
		if err := builtintools.AttachPromptSections(r, registry); err != nil {
			return nil, err
		}
		// Each web section is attached only when its tool is registered, so a
		// composition that disables one does not describe a tool the model
		// cannot call. The search text also branches on web_fetch visibility.
		_, haveSearch := registry.Lookup(toolweb.SearchToolName)
		_, haveFetch := registry.Lookup(toolweb.FetchToolName)
		if err := toolweb.AttachPromptSectionsFor(r, haveSearch, haveFetch); err != nil {
			return nil, err
		}
	}
	return r.Assemble(systemprompt.AssembleContext{})
}

func printPrompt(cfg runConfig) int {
	text, err := previewPrompt(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "qrpdsh:", err)
		return 1
	}
	fmt.Println(text)
	return 0
}

// previewPrompt renders the system prompt a turn WOULD send for cfg.
//
// It resolves the same home, working directory, plugin profile, and toolset a
// turn does: a preview that is assembled from different inputs than the real
// turn is worse than no preview, because it looks authoritative while
// describing a prompt the model will never see.
func previewPrompt(cfg runConfig) (string, error) {
	home, err := resolveHome(cfg.home)
	if err != nil {
		return "", err
	}
	workRoot := cfg.workDir
	if workRoot == "" {
		workRoot, _ = os.Getwd()
	}
	registry, _, err := buildToolset(workRoot, filepath.Join(home, "plugins"), cfg.allowShell)
	if err != nil {
		return "", err
	}
	assembly, err := assemblePrompt(cfg.persona, registry)
	if err != nil {
		return "", err
	}
	return systemprompt.Render(assembly)
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

	workRoot := cfg.workDir
	if workRoot == "" {
		workRoot, _ = os.Getwd()
	}
	profileDir := filepath.Join(home, "plugins")
	registry, _, err := buildToolset(workRoot, profileDir, cfg.allowShell)
	if err != nil {
		fmt.Fprintln(os.Stderr, "qrpdsh:", err)
		return 1
	}

	assembly, err := assemblePrompt(cfg.persona, registry)
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

// mountProfile composes a profile and mounts it onto the kernel.
//
// This is the end-to-end proof that the two halves connect: the loader's
// two-phase `!!js` evaluation runs against real scopes, so a row gated on
// `!ctx.get('profileContext')` is decided by the actual service registry rather
// than by a test fixture.
func mountProfile(profilePath, anchor, homeOverride string) int {
	if profilePath == "" {
		fmt.Fprintln(os.Stderr, "qrpdsh: -mount requires -profile")
		return 2
	}
	home, err := resolveHome(homeOverride)
	if err != nil {
		fmt.Fprintln(os.Stderr, "qrpdsh:", err)
		return 1
	}

	// The `!!js` evaluator runs in a Node sidecar. It is optional: without it,
	// any expression fails loudly rather than being treated as a literal.
	var evaluator runtime.Evaluator
	execRoot := ""
	if caps := workspace.Probe(home); caps.Usable() && caps.Executable {
		execRoot = home
	}
	pool, poolErr := jsbridge.NewPool(jsbridge.Config{
		ExecRoot:    execRoot,
		RequireRoot: filepath.Join(home, "plugins"),
	})
	if poolErr != nil {
		fmt.Fprintf(os.Stderr, "qrpdsh: no JavaScript evaluator (%v); !!js expressions will be refused\n", poolErr)
	} else {
		defer pool.Close()
		evaluator = &jsbridge.Evaluator{Pool: pool, Base: jsbridge.Scope{
			Home:    home,
			Helpers: []string{"dshHomePath"},
		}}
	}

	cwd, _ := os.Getwd()
	rt, err := runtime.Mount(context.Background(), profilePath, anchor, runtime.Options{
		Evaluator: evaluator,
		Home:      home,
		Cwd:       cwd,
		Env:       envMap(),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "qrpdsh:", err)
		return 1
	}
	defer rt.Dispose()

	fmt.Println(rt.Describe())
	for _, w := range rt.Warnings {
		fmt.Printf("  warning: %s\n", w)
	}
	// Print the rows that did NOT mount, since those are what an operator needs
	// to explain a missing capability.
	for _, row := range rt.Rows {
		if row.Skipped {
			fmt.Printf("  skipped %-44s %s\n", row.Entry.ID, row.Reason)
		}
	}
	return 0
}

// envMap snapshots the environment for `process.env` in expressions.
//
// Only the variables a config expression is expected to read are passed: the
// full environment would carry credentials into a JavaScript engine.
func envMap() map[string]string {
	out := map[string]string{}
	for _, key := range []string{
		"DSH_PERMISSION_MODE",
		"DSH_TELEMETRY_MODE",
		"DSH_TELEMETRY_OTLP_URL",
		"DSH_TOOLS_MODE",
		"DSH_SYSTEM_PROMPT",
		"DSH_CONTEXT_WINDOW",
		"DSH_MAX_TOKENS_AS_SUCCESS",
		"DSH_CLIENT_VERSION",
	} {
		if v, ok := os.LookupEnv(key); ok {
			out[key] = v
		}
	}
	return out
}
