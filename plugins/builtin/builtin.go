// Package builtinplugins maps the module specifier a composition row names to
// the Go plugin body this harness ships for it.
//
// WHY THIS IS A REGISTRY. A profile is data: it lists `@deepseek-ai/dsh-web` and
// knows nothing about Go import paths. That specifier is the only handle the
// loader has on an implementation, so runtime.Options.Registry keys bodies by it
// and this package is the single place the mapping lives. Adding a built-in is a
// change here and nowhere else.
//
// Nothing here is special-cased in the kernel. Each body is an ordinary cordis
// plugin with inject metadata, a config decode, and a provide, so the rows keep
// the DSH plugin mechanism (fiber, inject, config) instead of growing a second
// kind of plugin.
//
// WHAT A COMPOSITION MUST SUPPLY. The `tools` service that the tool-web row
// injects is provided by the profile's own `tools` row; this package consumes it
// and never creates it.
//
// PROVIDER ROWS register their provider on the `web` service and hand the
// disposer to ctx.Fiber().Effect, so a disabled or failed row withdraws its
// provider instead of leaving a stale entry auto-selection can pick. Row order
// matters: a provider row listed before the `web` row stays PENDING until the
// service arrives, which is the kernel's normal inject behavior, not an error.
package builtinplugins

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/qiyuan2403/Q_RP_dsh/runtime"
	"github.com/qiyuan2403/Q_RP_dsh/tools"
	toolweb "github.com/qiyuan2403/Q_RP_dsh/tools/web"
	"github.com/qiyuan2403/Q_RP_dsh/web"
	"github.com/qiyuan2403/Q_RP_dsh/web/deepseeksearch"
	"github.com/qiyuan2403/Q_RP_dsh/web/httpfetch"
	"github.com/qiyuan2403/Y_Cordis/cordis"
	"github.com/qiyuan2403/Y_Cordis/loader"
)

// The row names this package implements, copied verbatim from the official
// bundle patch (`packages/bundle/base/cordis.patch.yml`). They are the keys of
// Registry, so a typo shows up as a row that mounts without a body rather than
// as an import error.
const (
	// webRowName is the row that owns the `web` service.
	webRowName = "@deepseek-ai/dsh-web"
	// toolWebRowName is the row that registers the model-facing web tools.
	toolWebRowName = "@deepseek-ai/dsh-tool-web"
)

// Registry returns the Go plugin bodies this harness ships, keyed by the module
// specifier a composition row names.
//
// Every call returns a fresh map of factories, and every factory builds a fresh
// *cordis.Plugin. The kernel keys per-plugin runtime state by name and attaches
// per-fiber effects, so one shared instance would let one mount's resolved
// config or inject merge leak into another.
func Registry() map[string]runtime.PluginFactory {
	return map[string]runtime.PluginFactory{
		webRowName:            webPlugin,
		toolWebRowName:        toolWebPlugin,
		fetchHTTPRowName:      fetchHTTPPlugin,
		deepseekSearchRowName: deepseekSearchPlugin,
	}
}

// webPlugin builds the `@deepseek-ai/dsh-web` body: the provider-selecting
// `web` service, and nothing else.
//
// Provider registration belongs to separate rows, which is what lets a
// composition swap providers without touching this seam. The body therefore only
// constructs the service and provides it.
//
// The config is the official `{searchProvider?, fetchProvider?}`. No default is
// filled in here, and no environment variable is read here: web.New reads
// DSH_WEB_SEARCH_PROVIDER / DSH_WEB_FETCH_PROVIDER for an empty field, and a
// second fallback in this body would give the same setting two owners.
func webPlugin() *cordis.Plugin {
	return &cordis.Plugin{
		// The mount replaces Name with the row name, which is what diagnostics
		// and fiber identity use; a standalone Mount has no row, so the body
		// carries its own.
		Name:    "web",
		Provide: []string{"web"},
		Apply: func(ctx *cordis.Context, raw any) error {
			var cfg web.Config
			if err := decodeConfig(raw, &cfg); err != nil {
				return fmt.Errorf("builtinplugins: %s config: %w", webRowName, err)
			}
			if err := ctx.Provide("web", web.New(cfg), nil); err != nil {
				return fmt.Errorf("builtinplugins: %s: %w", webRowName, err)
			}
			return nil
		},
	}
}

// toolWebPlugin builds the `@deepseek-ai/dsh-tool-web` body: registration of
// `web_search` / `web_fetch` over the `web` service.
//
// INJECT DIFFERS FROM UPSTREAM BY ONE SERVICE, ON PURPOSE. Upstream declares
// `inject = ['tools', 'web', 'systemPrompt']` because its tool guidance is a
// `systemPrompt` contribution. Go's prompt assembly is not a service: the
// composition owner calls toolweb.AttachPromptSections while assembling the
// prompt, after the tools are registered and only when the composition exposes
// them. Declaring `systemPrompt` here would leave this row PENDING forever in
// any mount that has no such service, so the dependency was dropped and the
// attach call replaced it. Mount the tools, then attach the sections, then
// render.
func toolWebPlugin() *cordis.Plugin {
	return &cordis.Plugin{
		Name:   "tool-web",
		Inject: []string{"tools", "web"},
		Apply: func(ctx *cordis.Context, raw any) error {
			registry, err := service[*tools.Registry](ctx, "tools")
			if err != nil {
				return err
			}
			w, err := service[*web.Web](ctx, "web")
			if err != nil {
				return err
			}
			var wire toolWebConfig
			if err := decodeConfig(raw, &wire); err != nil {
				return fmt.Errorf("builtinplugins: %s config: %w", toolWebRowName, err)
			}
			if err := toolweb.Register(registry, w, wire.options()); err != nil {
				return fmt.Errorf("builtinplugins: %s: %w", toolWebRowName, err)
			}
			return nil
		},
	}
}

// service resolves a required service and checks its Go type.
//
// The type check is not distrust of this repository's own rows: cordis matches
// injections by NAME only, so any composition can satisfy `tools` with another
// plugin's service. Without the check a wrong type would panic inside the body,
// after the row was already reported ACTIVE, which points the operator at the
// consumer instead of at the mis-wired provider.
func service[T any](ctx *cordis.Context, name string) (T, error) {
	var zero T
	if ctx == nil {
		return zero, fmt.Errorf("builtinplugins: no context to resolve service %q", name)
	}
	value := ctx.Get(name)
	if value == nil {
		return zero, fmt.Errorf("builtinplugins: service %q is not available", name)
	}
	typed, ok := value.(T)
	if !ok {
		return zero, fmt.Errorf("builtinplugins: service %q is %T, want %T", name, value, zero)
	}
	return typed, nil
}

// toolWebConfig mirrors the official tool-web `Config` field for field.
//
// EVERY field is a pointer because the schema distinguishes "not configured"
// from "configured to the zero value". `search: false` must disable the tool,
// while an absent `search` must leave it enabled; decoding straight into
// toolweb.Options would collapse both cases to false on the Disable* fields.
//
// Durations stay in milliseconds, the unit the official schema uses
// (`fetchTimeoutMs`); options converts them to time.Duration.
type toolWebConfig struct {
	Search              *bool `json:"search"`
	Fetch               *bool `json:"fetch"`
	SearchMaxResults    *int  `json:"searchMaxResults"`
	SearchMaxQueries    *int  `json:"searchMaxQueries"`
	FetchTimeoutMs      *int  `json:"fetchTimeoutMs"`
	SearchTimeoutMs     *int  `json:"searchTimeoutMs"`
	FetchMaxOutputChars *int  `json:"fetchMaxOutputChars"`
}

// options maps the row config onto the registration options.
//
// An absent or explicitly zero limit is left at zero, which toolweb reads as
// "use the official default", for two reasons: this body would otherwise be a
// second copy of numbers toolweb already documents, and the copy would not be
// updated with it. Negative values are left for toolweb to refuse, so one place
// owns the validity rule.
func (c toolWebConfig) options() toolweb.Options {
	var opts toolweb.Options
	if c.Search != nil && !*c.Search {
		opts.DisableSearch = true
	}
	if c.Fetch != nil && !*c.Fetch {
		opts.DisableFetch = true
	}
	if c.SearchMaxResults != nil {
		opts.SearchMaxResults = *c.SearchMaxResults
	}
	if c.SearchMaxQueries != nil {
		opts.SearchMaxQueries = *c.SearchMaxQueries
	}
	if c.FetchTimeoutMs != nil {
		opts.FetchTimeout = time.Duration(*c.FetchTimeoutMs) * time.Millisecond
	}
	if c.SearchTimeoutMs != nil {
		opts.SearchTimeout = time.Duration(*c.SearchTimeoutMs) * time.Millisecond
	}
	if c.FetchMaxOutputChars != nil {
		opts.FetchMaxOutputChars = *c.FetchMaxOutputChars
	}
	return opts
}

// decodeConfig decodes a row's resolved config into out.
//
// The loader hands over its own tree types, not JSON, so a type assertion cannot
// work. Rather than walk the tree once per target struct (a second definition of
// every field name that would drift from the struct tags), the tree is converted
// to plain JSON values and encoding/json fills out: the tags stay the one schema,
// and unknown keys stay ignored the way the official schemastery object ignores
// them.
//
// Accepted inputs are the loader tree (*loader.Object, its map, slice, and
// scalar members), nil for "the row has no config", and plain JSON values a
// caller already decoded. Anything else is an error rather than a skipped field:
// silently defaulting a value the profile clearly set is the failure mode this
// helper exists to prevent.
//
// A nil config returns without touching out, which is what makes the pointer
// fields in the wire structs read as "not configured". Errors carry no prefix;
// the callers name the row, since only they know which row's config failed.
func decodeConfig(raw any, out any) error {
	if out == nil {
		return fmt.Errorf("decode target is nil")
	}
	if raw == nil {
		return nil
	}
	value, err := configValue(raw)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("config is not JSON-encodable: %w", err)
	}
	if err := json.Unmarshal(encoded, out); err != nil {
		return fmt.Errorf("config does not match %T: %w", out, err)
	}
	return nil
}

// configValue converts one loader value into its plain JSON counterpart.
//
// loader.Value is an ALIAS of any, so map[string]loader.Value arrives as
// map[string]any and []loader.Value as []any; one case each covers both
// spellings, and the comment is here so a future reader does not add a second
// unreachable case.
//
// The YAML dialect yields int64 for integers and float64 for floats; the
// jsbridge sidecar yields float64 for every `!!js` number. Both encode as JSON
// numbers, and encoding/json accepts an integral float64 for an int field, which
// is how a `!!js` timeout reaches an integer config field.
func configValue(raw any) (any, error) {
	switch v := raw.(type) {
	case nil, bool, string, int, int64, float64:
		return v, nil
	case *loader.Object:
		out := make(map[string]any, v.Len())
		for _, key := range v.Keys() {
			member, _ := v.Get(key)
			converted, err := configValue(member)
			if err != nil {
				return nil, fmt.Errorf("config member %q: %w", key, err)
			}
			out[key] = converted
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, member := range v {
			converted, err := configValue(member)
			if err != nil {
				return nil, fmt.Errorf("config member %q: %w", key, err)
			}
			out[key] = converted
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, member := range v {
			converted, err := configValue(member)
			if err != nil {
				return nil, fmt.Errorf("config item %d: %w", i, err)
			}
			out[i] = converted
		}
		return out, nil
	default:
		return nil, fmt.Errorf("config value of type %T is not a loader value", raw)
	}
}

// Provider rows.
//
// The two providers the official base bundle ships register themselves on the
// `web` service instead of owning it: the row injects `web`, constructs its
// provider, and hands the registration disposer to the fiber's effect list. That
// is what makes a disabled or failed provider row withdraw its provider again
// instead of leaving a stale entry that auto-selection can pick.
const (
	// fetchHTTPRowName is the anonymous public HTTP(S) fetch provider row.
	fetchHTTPRowName = "@deepseek-ai/dsh-web-fetch-http"
	// deepseekSearchRowName is the DeepSeek-backed search provider row.
	deepseekSearchRowName = "@deepseek-ai/dsh-web-search-deepseek"
)

// fetchHTTPPlugin builds the `@deepseek-ai/dsh-web-fetch-http` body.
//
// The upstream config is size and timeout policy for one anonymous HTTP client;
// it is translated field for field, with the millisecond timeout converted at
// the boundary because the wire format is milliseconds and Go wants a Duration.
func fetchHTTPPlugin() *cordis.Plugin {
	return &cordis.Plugin{
		Name:   "web-fetch-http",
		Inject: []string{"web"},
		Apply: func(ctx *cordis.Context, raw any) error {
			w, err := service[*web.Web](ctx, "web")
			if err != nil {
				return err
			}
			var wire fetchHTTPConfig
			if err := decodeConfig(raw, &wire); err != nil {
				return fmt.Errorf("builtinplugins: %s config: %w", fetchHTTPRowName, err)
			}
			provider, err := httpfetch.New(wire.providerConfig())
			if err != nil {
				return fmt.Errorf("builtinplugins: %s: %w", fetchHTTPRowName, err)
			}
			return effect(ctx, func() (func(), error) {
				return w.RegisterFetchProvider(provider)
			})
		},
	}
}

// deepseekSearchPlugin builds the `@deepseek-ai/dsh-web-search-deepseek` body.
//
// The provider reads its own environment defaults for the API key and endpoint
// (DEEPSEEK_API_KEY, DEEPSEEK_SEARCH_BASE_URL), so the row config is a set of
// overrides and an empty config still activates: the provider reports itself
// unavailable until a key exists, and the seam reports that as a structured
// selection error rather than as a missing provider.
func deepseekSearchPlugin() *cordis.Plugin {
	return &cordis.Plugin{
		Name:   "web-search-deepseek",
		Inject: []string{"web"},
		Apply: func(ctx *cordis.Context, raw any) error {
			w, err := service[*web.Web](ctx, "web")
			if err != nil {
				return err
			}
			var wire deepseekSearchConfig
			if err := decodeConfig(raw, &wire); err != nil {
				return fmt.Errorf("builtinplugins: %s config: %w", deepseekSearchRowName, err)
			}
			provider, err := deepseeksearch.New(wire.providerConfig())
			if err != nil {
				return fmt.Errorf("builtinplugins: %s: %w", deepseekSearchRowName, err)
			}
			return effect(ctx, func() (func(), error) {
				return w.RegisterSearchProvider(provider)
			})
		},
	}
}

// effect runs register and ties its disposer to the calling fiber.
//
// A provider registration that outlives its row is worse than a missing one: the
// seam would keep selecting a provider whose configuration or credentials the
// row owned, and a later row could not register the same id. cordis runs effect
// disposers LIFO at unload, so the provider is withdrawn with the row.
func effect(ctx *cordis.Context, register func() (func(), error)) error {
	if ctx == nil {
		return fmt.Errorf("builtinplugins: no context for provider effect")
	}
	return ctx.Fiber().Effect(func() (cordis.Disposer, error) {
		dispose, err := register()
		if err != nil {
			return nil, err
		}
		return func() error {
			dispose()
			return nil
		}, nil
	})
}

// fetchHTTPConfig mirrors the official web-fetch-http `Config`.
//
// Pointers distinguish "not configured" from a configured zero; a negative
// value is passed through to the provider, which owns the validity rule.
type fetchHTTPConfig struct {
	MaxResponseBytes *int64  `json:"maxResponseBytes"`
	MaxBodyChars     *int    `json:"maxBodyChars"`
	TimeoutMs        *int    `json:"timeoutMs"`
	MaxRedirects     *int    `json:"maxRedirects"`
	UserAgent        *string `json:"userAgent"`
}

// providerConfig maps the row config onto the provider's Go config.
func (c fetchHTTPConfig) providerConfig() httpfetch.Config {
	var cfg httpfetch.Config
	if c.MaxResponseBytes != nil {
		cfg.MaxResponseBytes = *c.MaxResponseBytes
	}
	if c.MaxBodyChars != nil {
		cfg.MaxBodyChars = *c.MaxBodyChars
	}
	if c.TimeoutMs != nil {
		cfg.Timeout = time.Duration(*c.TimeoutMs) * time.Millisecond
	}
	// The provider's field is a pointer because zero has an upstream meaning
	// ("follow no redirects") that must stay distinguishable from "not
	// configured"; the row config uses the same encoding, so it passes through.
	cfg.MaxRedirects = c.MaxRedirects
	if c.UserAgent != nil {
		cfg.UserAgent = *c.UserAgent
	}
	return cfg
}

// deepseekSearchConfig mirrors the official web-search-deepseek `Config`.
//
// `apiKeyEnv` names where to read the key rather than carrying it, which keeps a
// secret out of the profile file; a literal `apiKey` wins when both are set,
// matching the provider's own precedence.
type deepseekSearchConfig struct {
	APIKey     *string `json:"apiKey"`
	APIKeyEnv  *string `json:"apiKeyEnv"`
	BaseURL    *string `json:"baseURL"`
	Model      *string `json:"model"`
	APIVersion *string `json:"apiVersion"`
	MaxTokens  *int    `json:"maxTokens"`
	MaxUses    *int    `json:"maxUses"`
}

// providerConfig maps the row config onto the provider's Go config.
func (c deepseekSearchConfig) providerConfig() deepseeksearch.Config {
	var cfg deepseeksearch.Config
	if c.APIKey != nil {
		cfg.APIKey = *c.APIKey
	}
	if c.APIKeyEnv != nil {
		cfg.APIKeyEnv = *c.APIKeyEnv
	}
	if c.BaseURL != nil {
		cfg.BaseURL = *c.BaseURL
	}
	if c.Model != nil {
		cfg.Model = *c.Model
	}
	if c.APIVersion != nil {
		cfg.APIVersion = *c.APIVersion
	}
	if c.MaxTokens != nil {
		cfg.MaxTokens = *c.MaxTokens
	}
	if c.MaxUses != nil {
		cfg.MaxUses = *c.MaxUses
	}
	return cfg
}
