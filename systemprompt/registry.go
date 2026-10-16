// Package systemprompt assembles the system prompt and the tool schemas.
//
// COMPATIBILITY. The ordering tables, the `{{variable}}` interpolation rules,
// and the tool-order semantics are aligned to the official
// `@deepseek-ai/dsh-system-prompt` (packages/core/system-prompt/src/index.ts).
// The tables are copied verbatim because their VALUES are the contract: two
// sections at the same number fall back to name order, so a renumbered table
// silently reorders every prompt.
//
// WHY A REGISTRY RATHER THAN A STRING. Plugins contribute sections, runtime
// facts, tools, and variables independently, and the composition decides what
// is present. A single assembled string could not express that, and a plugin
// could not add to it without the composition being rebuilt.
package systemprompt

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// SectionOrders are the centrally allocated prompt-section positions.
//
// Copied verbatim from the official `SECTION_ORDERS`. A plugin that picks its
// own number is asserting a position relative to these, which is why they are
// published rather than private.
var SectionOrders = map[string]int{
	"HARNESS_IDENTITY":            -1000,
	"DEPLOYMENT_PERSONA_PREFIX":   0,
	"PLAN_POLICY":                 500,
	"TEAM_POLICY":                 600,
	"PTC_ONLY":                    800,
	"FILE_REFERENCE":              900,
	"TOOL_BASH":                   1000,
	"TOOL_PWSH":                   1010,
	"TOOL_READ":                   1100,
	"TOOL_WRITE":                  1200,
	"TOOL_EDIT":                   1300,
	"TOOL_GLOB":                   1400,
	"TOOL_GREP":                   1500,
	"TOOL_JOBS":                   1600,
	"TOOL_PTY":                    1700,
	"TOOL_WEB_SEARCH":             2000,
	"TOOL_WEB_FETCH":              2100,
	"TOOL_LSP":                    2200,
	"TOOL_SESSION_QUERY":          2300,
	"TOOL_GOAL":                   2400,
	"TOOL_WORKFLOW":               2600,
	"TOOL_RALPH":                  2700,
	"TOOL_SUBAGENT":               2800,
	"TOOL_REPORT":                 2900,
	"TOOL_COMPUTER_USE":           3000,
	"MCP_SERVERS":                 3100,
	"TOOLS_SDK":                   5000,
	"DELIVERABLE_FILE_REFERENCES": 9000,
	"STRUCTURED_OUTPUT":           9900,
	// Local paths and endpoints follow reusable instructions.
	"HARNESS_SOURCE":            10000,
	"WEB_SURFACE":               10100,
	"DEPLOYMENT_PERSONA_SUFFIX": 10200,
}

// ContextOrders are the centrally allocated runtime-context positions.
//
// Process-specific paths and endpoints sit here rather than in the cached
// system prompt, which is why the two tables are separate.
var ContextOrders = map[string]int{
	"WORKING_DIRECTORY":   100,
	"SANDBOX_POLICY":      110,
	"APPROVAL_POLICY":     115,
	"SUBAGENT_DELEGATION": 120,
	"TEAM_IDENTITY":       125,
	"HARNESS_SOURCE":      200,
	"WEB_SURFACE":         210,
}

// Section names that a composition may replace, so both the deployment and an
// agent preset name the SAME slot. Sharing the name is what makes a
// replacement replace rather than duplicate.
const (
	// PersonaPrefixSection is the deployment persona prefix slot.
	PersonaPrefixSection = "deployment:persona-prefix"
	// PersonaSuffixSection is the deployment persona suffix slot.
	PersonaSuffixSection = "deployment:persona-suffix"
)

// ToolOrderRest is the reserved marker for unlisted tools in a configured tool
// order.
const ToolOrderRest = "<unlisted-tools>"

// Errors the registry reports.
var (
	// ErrDuplicateSection reports two sections with the same name.
	ErrDuplicateSection = errors.New("systemprompt: section name is already registered")
	// ErrDuplicateContext reports two contexts with the same name.
	ErrDuplicateContext = errors.New("systemprompt: context name is already registered")
	// ErrUnknownVariable reports a `{{name}}` with no registered variable.
	ErrUnknownVariable = errors.New("systemprompt: unknown prompt variable")
	// ErrMalformedReference reports a `{{...}}` that is not a complete simple
	// group.
	ErrMalformedReference = errors.New("systemprompt: malformed prompt variable reference")
	// ErrMultipleCompleteSections reports more than one effective complete
	// section, which makes the prompt ambiguous.
	ErrMultipleCompleteSections = errors.New("systemprompt: more than one complete section")
)

// AssembleContext is the per-assembly input.
type AssembleContext struct {
	// Scope selects which providers participate. Empty means global only.
	Scope string
}

// Section is one contributed prompt section.
type Section struct {
	// Name is unique; a duplicate registration is refused.
	Name string
	// Order places the section. Equal orders fall back to name order.
	Order int
	// Text is the section body, or a provider evaluated at each assembly.
	Text string
	// Provider, when set, supplies the text and takes precedence over Text.
	Provider func(AssembleContext) string
	// Interpolate controls `{{variable}}` substitution. A nil pointer means the
	// default (true); false preserves literal text.
	Interpolate *bool
	// Complete marks this contribution as the WHOLE system prompt.
	//
	// Assembly still resolves tools and variables, then restores this section as
	// the only prompt section. More than one effective complete section fails
	// assembly rather than picking one.
	Complete bool
}

// Context is one dynamic runtime fact, materialized as a durable snapshot.
type Context struct {
	// Name is unique.
	Name string
	// Order places the context.
	Order int
	// Text is the context body, or a provider evaluated at each assembly. Empty
	// text contributes nothing.
	Text string
	// Provider, when set, supplies the text and takes precedence over Text.
	Provider func(AssembleContext) string
	// Interpolate controls substitution; nil means the default (true).
	Interpolate *bool
	// Required keeps this context when optional runtime context is disabled.
	Required bool
}

// ToolSchema is one tool definition visible to the model.
type ToolSchema struct {
	Name        string
	Description string
	// Parameters is a JSON Schema object.
	Parameters []byte
}

// AssembledSection is a section with its text resolved (but not interpolated).
type AssembledSection struct {
	Name        string
	Text        string
	Interpolate *bool
}

// AssembledContext is a resolved dynamic context contribution.
type AssembledContext struct {
	Name        string
	Text        string
	Interpolate *bool
}

// Assembly is the assembled model input.
type Assembly struct {
	Sections  []AssembledSection
	Contexts  []AssembledContext
	Tools     []ToolSchema
	Variables map[string]string
}

// Registry holds the prompt contributions.
type Registry struct {
	sections  map[string]Section
	contexts  map[string]Context
	variables map[string]func(AssembleContext) string
	tools     []ToolSchema
	// toolKnown is the pre-restriction name universe used to validate a
	// configured tool order.
	toolKnown map[string]bool
	// toolOrder is the configured order, validated for duplicates and for the
	// required rest marker at configuration time.
	toolOrder []string
	// includeHarnessIdentity controls the fixed identity section.
	includeHarnessIdentity bool
	// includeRuntimeContext controls whether optional contexts are emitted.
	includeRuntimeContext bool
}

// New returns an empty registry with the default configuration.
func New() *Registry {
	return &Registry{
		sections:               map[string]Section{},
		contexts:               map[string]Context{},
		variables:              map[string]func(AssembleContext) string{},
		toolKnown:              map[string]bool{},
		includeHarnessIdentity: true,
		includeRuntimeContext:  true,
	}
}

// SetIncludeHarnessIdentity controls the fixed identity section.
func (r *Registry) SetIncludeHarnessIdentity(v bool) { r.includeHarnessIdentity = v }

// SetIncludeRuntimeContext controls optional runtime-context snapshots.
// Required contexts are emitted regardless.
func (r *Registry) SetIncludeRuntimeContext(v bool) { r.includeRuntimeContext = v }

// Section registers a prompt section.
func (r *Registry) Section(s Section) error {
	if strings.TrimSpace(s.Name) == "" {
		return errors.New("systemprompt: section name is required")
	}
	if s.Text == "" && s.Provider == nil {
		return fmt.Errorf("systemprompt: section %q has no text or provider", s.Name)
	}
	if _, exists := r.sections[s.Name]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateSection, s.Name)
	}
	r.sections[s.Name] = s
	return nil
}

// Context registers a runtime fact.
func (r *Registry) Context(c Context) error {
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("systemprompt: context name is required")
	}
	if _, exists := r.contexts[c.Name]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateContext, c.Name)
	}
	r.contexts[c.Name] = c
	return nil
}

// Variable registers a `{{name}}` provider.
//
// The name is validated because a malformed one can never be referenced, and
// discovering that at render time is worse than at registration.
func (r *Registry) Variable(name string, provider func(AssembleContext) string) error {
	if !validVariableName(name) {
		return fmt.Errorf("systemprompt: invalid prompt variable name %q", name)
	}
	r.variables[name] = provider
	return nil
}

// SetTools replaces the tool schemas and their known-name universe.
//
// The two are separate because a configured order may list a tool that a
// restriction has removed from THIS assembly: the name is still known, so the
// order stays valid.
func (r *Registry) SetTools(schemas []ToolSchema, knownNames []string) error {
	r.tools = append([]ToolSchema(nil), schemas...)
	r.toolKnown = map[string]bool{}
	for _, n := range knownNames {
		r.toolKnown[n] = true
	}
	for _, s := range schemas {
		r.toolKnown[s.Name] = true
	}
	for _, s := range schemas {
		if s.Name == ToolOrderRest {
			return fmt.Errorf("systemprompt: a tool provider returned the reserved name %q", ToolOrderRest)
		}
	}
	if r.toolOrder != nil {
		return r.validateToolOrder()
	}
	return nil
}

// SetToolOrder configures the model-facing tool order.
//
// The list must contain ToolOrderRest, which marks where unlisted tools are
// inserted: without it there is no defined place for a tool the list omits, so
// the order would silently drop it.
func (r *Registry) SetToolOrder(order []string) error {
	if order == nil {
		r.toolOrder = nil
		return nil
	}
	seen := map[string]bool{}
	for _, name := range order {
		if seen[name] {
			return fmt.Errorf("systemprompt: toolOrder lists %q more than once", name)
		}
		seen[name] = true
	}
	if !seen[ToolOrderRest] {
		return fmt.Errorf("systemprompt: toolOrder must contain the %q rest entry", ToolOrderRest)
	}
	r.toolOrder = append([]string(nil), order...)
	return r.validateToolOrder()
}

func (r *Registry) validateToolOrder() error {
	var unknown []string
	for _, name := range r.toolOrder {
		if name == ToolOrderRest {
			continue
		}
		if !r.toolKnown[name] {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		known := make([]string, 0, len(r.toolKnown))
		for n := range r.toolKnown {
			known = append(known, n)
		}
		sort.Strings(known)
		if len(known) == 0 {
			return fmt.Errorf("systemprompt: toolOrder lists unregistered tools %s; no tools are known", quoteAll(unknown))
		}
		return fmt.Errorf("systemprompt: toolOrder lists unregistered tools %s; known tools: %s",
			quoteAll(unknown), strings.Join(known, ", "))
	}
	return nil
}

// Assemble builds the model input for one request.
//
// The steps mirror the official assembly: resolve each section and context
// (providers run HERE, with the assembly's context), order deterministically,
// order the tools, and resolve the variables. Interpolation happens at render
// time, not here, because a section's text is also stored and diffed.
func (r *Registry) Assemble(ctx AssembleContext) (*Assembly, error) {
	out := &Assembly{Variables: map[string]string{}}

	sections := make([]Section, 0, len(r.sections))
	for _, s := range r.sections {
		sections = append(sections, s)
	}
	sort.Slice(sections, func(i, j int) bool {
		if sections[i].Order != sections[j].Order {
			return sections[i].Order < sections[j].Order
		}
		// Code-unit comparison, so the order is identical on every machine.
		return sections[i].Name < sections[j].Name
	})

	var complete []string
	for _, s := range sections {
		text := s.Text
		if s.Provider != nil {
			text = s.Provider(ctx)
		}
		if s.Complete {
			complete = append(complete, s.Name)
		}
		out.Sections = append(out.Sections, AssembledSection{
			Name:        s.Name,
			Text:        text,
			Interpolate: s.Interpolate,
		})
	}
	if len(complete) > 1 {
		return nil, fmt.Errorf("%w: %s", ErrMultipleCompleteSections, strings.Join(complete, ", "))
	}
	// A complete section is restored as the ONLY prompt section. It is kept even
	// when its text is empty: the author said it is the whole prompt, and
	// dropping it would silently fall back to the full assembly.
	if len(complete) == 1 {
		for _, s := range out.Sections {
			if s.Name == complete[0] {
				out.Sections = []AssembledSection{s}
				break
			}
		}
	}

	contexts := make([]Context, 0, len(r.contexts))
	for _, c := range r.contexts {
		contexts = append(contexts, c)
	}
	sort.Slice(contexts, func(i, j int) bool {
		if contexts[i].Order != contexts[j].Order {
			return contexts[i].Order < contexts[j].Order
		}
		return contexts[i].Name < contexts[j].Name
	})
	for _, c := range contexts {
		if !r.includeRuntimeContext && !c.Required {
			continue
		}
		text := c.Text
		if c.Provider != nil {
			text = c.Provider(ctx)
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		out.Contexts = append(out.Contexts, AssembledContext{
			Name:        c.Name,
			Text:        text,
			Interpolate: c.Interpolate,
		})
	}

	out.Tools = r.orderTools()

	for name, provider := range r.variables {
		out.Variables[name] = provider(ctx)
	}
	return out, nil
}

// orderTools applies the configured order, or sorts by name when none is set.
func (r *Registry) orderTools() []ToolSchema {
	tools := append([]ToolSchema(nil), r.tools...)
	byName := func(i, j int) bool { return tools[i].Name < tools[j].Name }
	sort.Slice(tools, byName)

	if r.toolOrder == nil {
		return tools
	}
	listed := map[string]bool{}
	for _, name := range r.toolOrder {
		listed[name] = true
	}
	var rest []ToolSchema
	for _, t := range tools {
		if !listed[t.Name] {
			rest = append(rest, t)
		}
	}
	var out []ToolSchema
	for _, name := range r.toolOrder {
		if name == ToolOrderRest {
			out = append(out, rest...)
			continue
		}
		for _, t := range tools {
			if t.Name == name {
				out = append(out, t)
			}
		}
	}
	return out
}

// Render joins the sections into the final system prompt.
//
// An empty section contributes nothing, and the non-empty ones are separated by
// a blank line, so a plugin cannot accidentally splice two sections into one
// paragraph.
func Render(a *Assembly) (string, error) {
	parts := make([]string, 0, len(a.Sections))
	for _, s := range a.Sections {
		text := s.Text
		if s.Interpolate == nil || *s.Interpolate {
			var err error
			text, err = interpolate(s.Name, "section", s.Text, a.Variables)
			if err != nil {
				return "", err
			}
		}
		if len(text) > 0 {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

// RenderContext joins the runtime-context snapshots.
func RenderContext(a *Assembly) (string, error) {
	parts := make([]string, 0, len(a.Contexts))
	for _, c := range a.Contexts {
		text := c.Text
		if c.Interpolate == nil || *c.Interpolate {
			var err error
			text, err = interpolate(c.Name, "context", c.Text, a.Variables)
			if err != nil {
				return "", err
			}
		}
		if len(text) > 0 {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

// interpolate replaces `{{name}}` references.
//
// The rules are the official ones and each matters:
//
//   - Only a COMPLETE `{{name}}` group is a reference. A `{{` with no matching
//     `}}` is literal prose, but a `{{` with a LATER `}}` is a malformed
//     reference and fails — the author clearly meant a variable.
//   - The name must match `^[a-z][a-z0-9_]*$`; `{{}}` is malformed.
//   - An unregistered name fails. Substituting an empty string would silently
//     drop a requirement the prompt was supposed to state.
//   - A registered name with no value for THIS assembly also fails.
func interpolate(owner, kind, text string, variables map[string]string) (string, error) {
	var b strings.Builder
	last := 0
	for {
		open := strings.Index(text[last:], "{{")
		if open < 0 {
			break
		}
		open += last
		rest := text[open:]
		end := strings.Index(rest, "}}")
		// A complete group is `{{`, a name, then `}}`. The name portion starts
		// AFTER the opening braces: testing the whole prefix would always find
		// the `{{` being matched on and misreport every reference as malformed.
		//
		// No separate brace check is needed here. The official grammar excludes
		// braces from the name (`[^{}]*`), but the identifier rule below already
		// rejects them, so a name containing a brace fails either way.
		if end >= 2 {
			name := rest[2:end]
			if !validVariableName(name) {
				return "", fmt.Errorf("%w: \"{{%s}}\" in %s %q (variable names match ^[a-z][a-z0-9_]*$)",
					ErrMalformedReference, name, kind, owner)
			}
			value, ok := variables[name]
			if !ok {
				known := sortedKeys(variables)
				if len(known) == 0 {
					return "", fmt.Errorf("%w: \"{{%s}}\" in %s %q; no variables are registered",
						ErrUnknownVariable, name, kind, owner)
				}
				return "", fmt.Errorf("%w: \"{{%s}}\" in %s %q; registered variables: %s",
					ErrUnknownVariable, name, kind, owner, strings.Join(known, ", "))
			}
			b.WriteString(text[last:open])
			b.WriteString(value)
			last = open + end + 2
			continue
		}
		// A later `}}` makes this a malformed reference; otherwise it is prose.
		if strings.Contains(text[open+2:], "}}") {
			return "", fmt.Errorf("%w: \"%s\" in %s %q (references are complete simple {{name}} groups)",
				ErrMalformedReference, snippet(text[open:]), kind, owner)
		}
		b.WriteString(text[last : open+2])
		last = open + 2
	}
	b.WriteString(text[last:])
	return b.String(), nil
}

func snippet(s string) string {
	if len(s) > 16 {
		return s[:16] + "..."
	}
	return s
}

// validVariableName reports whether a name matches `^[a-z][a-z0-9_]*$`.
func validVariableName(name string) bool {
	if name == "" {
		return false
	}
	if name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for i := 1; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '_':
		default:
			return false
		}
	}
	return true
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func quoteAll(names []string) string {
	quoted := make([]string, 0, len(names))
	for _, n := range names {
		quoted = append(quoted, fmt.Sprintf("%q", n))
	}
	return strings.Join(quoted, ", ")
}
