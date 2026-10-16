package systemprompt

import (
	"errors"
	"strings"
	"testing"
)

// TestSectionsAreOrderedByNumberThenName pins the ordering contract: an
// explicit order wins, and equal orders fall back to name order so the result
// is identical on every machine.
func TestSectionsAreOrderedByNumberThenName(t *testing.T) {
	r := New()
	for _, s := range []Section{
		{Name: "zeta", Order: 10, Text: "z"},
		{Name: "alpha", Order: 10, Text: "a"},
		{Name: "first", Order: -1000, Text: "f"},
		{Name: "middle", Order: 0, Text: "m"},
	} {
		if err := r.Section(s); err != nil {
			t.Fatalf("Section(%s): %v", s.Name, err)
		}
	}
	as, err := r.Assemble(AssembleContext{})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	got := make([]string, 0, len(as.Sections))
	for _, s := range as.Sections {
		got = append(got, s.Name)
	}
	want := []string{"first", "middle", "alpha", "zeta"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

// TestDuplicateSectionRefused pins that a second registration under the same
// name fails rather than silently replacing.
func TestDuplicateSectionRefused(t *testing.T) {
	r := New()
	if err := r.Section(Section{Name: "s", Text: "a"}); err != nil {
		t.Fatalf("Section: %v", err)
	}
	if err := r.Section(Section{Name: "s", Text: "b"}); !errors.Is(err, ErrDuplicateSection) {
		t.Fatalf("error = %v, want ErrDuplicateSection", err)
	}
}

// TestSectionRequiresTextOrProvider pins fail-loud on an empty contribution.
func TestSectionRequiresTextOrProvider(t *testing.T) {
	r := New()
	if err := r.Section(Section{Name: "s"}); err == nil {
		t.Fatal("a section with no text or provider was accepted")
	}
	if err := r.Section(Section{Text: "x"}); err == nil {
		t.Fatal("a section with no name was accepted")
	}
}

// TestProviderRunsAtAssemblyTime pins that a dynamic section is evaluated per
// assembly, which is how runtime facts reach the prompt.
func TestProviderRunsAtAssemblyTime(t *testing.T) {
	r := New()
	calls := 0
	if err := r.Section(Section{
		Name:  "dynamic",
		Order: 1,
		Provider: func(ctx AssembleContext) string {
			calls++
			return "scope=" + ctx.Scope
		},
	}); err != nil {
		t.Fatalf("Section: %v", err)
	}
	as, err := r.Assemble(AssembleContext{Scope: "agent-1"})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if as.Sections[0].Text != "scope=agent-1" {
		t.Fatalf("Text = %q", as.Sections[0].Text)
	}
	if calls != 1 {
		t.Fatalf("provider called %d times, want 1", calls)
	}
}

// TestRenderJoinsSectionsWithBlankLines pins the separator: two sections must
// not splice into one paragraph.
func TestRenderJoinsSectionsWithBlankLines(t *testing.T) {
	r := New()
	_ = r.Section(Section{Name: "a", Order: 1, Text: "first"})
	_ = r.Section(Section{Name: "b", Order: 2, Text: "second"})
	as, _ := r.Assemble(AssembleContext{})
	got, err := Render(as)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got != "first\n\nsecond" {
		t.Fatalf("Render = %q", got)
	}
}

// TestEmptySectionsAreDropped pins that a section resolving to empty text
// contributes nothing rather than an empty paragraph.
func TestEmptySectionsAreDropped(t *testing.T) {
	r := New()
	_ = r.Section(Section{Name: "empty", Order: 1, Text: ""})
	_ = r.Section(Section{Name: "real", Order: 2, Text: "content"})
	as, err := r.Assemble(AssembleContext{})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	got, _ := Render(as)
	if got != "content" {
		t.Fatalf("Render = %q", got)
	}
}

// TestVariableInterpolation pins the happy path.
func TestVariableInterpolation(t *testing.T) {
	r := New()
	if err := r.Variable("cwd", func(AssembleContext) string { return "/work" }); err != nil {
		t.Fatalf("Variable: %v", err)
	}
	_ = r.Section(Section{Name: "s", Order: 1, Text: "working dir: {{cwd}}"})
	as, _ := r.Assemble(AssembleContext{})
	got, err := Render(as)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got != "working dir: /work" {
		t.Fatalf("Render = %q", got)
	}
}

// TestUnknownVariableFailsRender pins that an unregistered reference fails
// rather than substituting an empty string: silently dropping a variable would
// drop a requirement the prompt was meant to state.
func TestUnknownVariableFailsRender(t *testing.T) {
	r := New()
	_ = r.Section(Section{Name: "s", Order: 1, Text: "missing {{nope}}"})
	as, _ := r.Assemble(AssembleContext{})
	if _, err := Render(as); !errors.Is(err, ErrUnknownVariable) {
		t.Fatalf("error = %v, want ErrUnknownVariable", err)
	}
}

// TestMalformedReferenceFails pins the reference grammar.
func TestMalformedReferenceFails(t *testing.T) {
	cases := map[string]string{
		"empty name":    "a {{}} b",
		"uppercase":     "a {{Bad}} b",
		"leading digit": "a {{1bad}} b",
		"dash":          "a {{a-b}} b",
		// A name containing braces is not a simple group. This is the case
		// that distinguishes "the name must not contain braces" from "the
		// name must merely be a valid identifier": the inner `{` fails the
		// identifier rule too, but a scanner that takes everything up to the
		// FIRST `}}` would hand `{a` to that rule and could accept a longer
		// name by accident.
		"braces in name": "a {{a{b}} c",
	}
	for label, text := range cases {
		r := New()
		_ = r.Variable("ok", func(AssembleContext) string { return "v" })
		_ = r.Section(Section{Name: "s", Order: 1, Text: text})
		as, err := r.Assemble(AssembleContext{})
		if err != nil {
			t.Fatalf("%s: Assemble: %v", label, err)
		}
		if _, err := Render(as); !errors.Is(err, ErrMalformedReference) {
			t.Fatalf("%s: error = %v, want ErrMalformedReference", label, err)
		}
	}
}

// TestLiteralBracesWithoutClosingAreProse pins that a `{{` with no `}}` at all
// is ordinary text, not an error: a prompt may legitimately contain braces.
func TestLiteralBracesWithoutClosingAreProse(t *testing.T) {
	r := New()
	_ = r.Section(Section{Name: "s", Order: 1, Text: "use {{ to open a group"})
	as, _ := r.Assemble(AssembleContext{})
	got, err := Render(as)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got != "use {{ to open a group" {
		t.Fatalf("Render = %q", got)
	}
}

// TestInterpolateFalsePreservesLiteralText pins the opt-out, which a section
// documenting the syntax itself needs.
func TestInterpolateFalsePreservesLiteralText(t *testing.T) {
	r := New()
	_ = r.Variable("cwd", func(AssembleContext) string { return "/work" })
	_ = r.Section(Section{Name: "s", Order: 1, Text: "literal {{cwd}}", Interpolate: new(false)})
	as, _ := r.Assemble(AssembleContext{})
	got, err := Render(as)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got != "literal {{cwd}}" {
		t.Fatalf("Render = %q, want the reference preserved", got)
	}
}

// TestCompleteSectionBecomesTheWholePrompt pins the complete-section contract:
// assembly still runs, then that section is restored as the only one.
func TestCompleteSectionBecomesTheWholePrompt(t *testing.T) {
	r := New()
	_ = r.Section(Section{Name: "identity", Order: -1000, Text: "harness identity"})
	_ = r.Section(Section{Name: "tools", Order: 1000, Text: "tool guidance"})
	_ = r.Section(Section{Name: "mine", Order: 1, Text: "ONLY THIS", Complete: true})

	as, err := r.Assemble(AssembleContext{})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	got, err := Render(as)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got != "ONLY THIS" {
		t.Fatalf("Render = %q, want only the complete section", got)
	}
}

// TestMultipleCompleteSectionsFail pins that an ambiguous prompt fails rather
// than picking one.
func TestMultipleCompleteSectionsFail(t *testing.T) {
	r := New()
	_ = r.Section(Section{Name: "a", Order: 1, Text: "A", Complete: true})
	_ = r.Section(Section{Name: "b", Order: 2, Text: "B", Complete: true})
	if _, err := r.Assemble(AssembleContext{}); !errors.Is(err, ErrMultipleCompleteSections) {
		t.Fatalf("error = %v, want ErrMultipleCompleteSections", err)
	}
}

// TestContextsAreSeparateFromSections pins that runtime facts do not enter the
// system prompt: they are materialized as their own snapshot.
func TestContextsAreSeparateFromSections(t *testing.T) {
	r := New()
	_ = r.Section(Section{Name: "s", Order: 1, Text: "prompt"})
	_ = r.Context(Context{Name: "cwd", Order: 100, Text: "/work"})

	as, err := r.Assemble(AssembleContext{})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	prompt, _ := Render(as)
	if strings.Contains(prompt, "/work") {
		t.Fatalf("a context leaked into the system prompt: %q", prompt)
	}
	ctx, err := RenderContext(as)
	if err != nil {
		t.Fatalf("RenderContext: %v", err)
	}
	if ctx != "/work" {
		t.Fatalf("RenderContext = %q", ctx)
	}
}

// TestOptionalContextsCanBeDisabledButRequiredStay pins the required flag.
func TestOptionalContextsCanBeDisabledButRequiredStay(t *testing.T) {
	r := New()
	r.SetIncludeRuntimeContext(false)
	_ = r.Context(Context{Name: "optional", Order: 1, Text: "optional text"})
	_ = r.Context(Context{Name: "required", Order: 2, Text: "required text", Required: true})

	as, err := r.Assemble(AssembleContext{})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(as.Contexts) != 1 || as.Contexts[0].Name != "required" {
		t.Fatalf("contexts = %+v, want only the required one", as.Contexts)
	}
}

// TestEmptyContextContributesNothing pins that a provider returning empty text
// produces no snapshot.
func TestEmptyContextContributesNothing(t *testing.T) {
	r := New()
	_ = r.Context(Context{Name: "empty", Order: 1, Provider: func(AssembleContext) string { return "   " }})
	as, err := r.Assemble(AssembleContext{})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(as.Contexts) != 0 {
		t.Fatalf("contexts = %+v, want none", as.Contexts)
	}
}

// TestToolsSortByNameWithoutConfiguration pins the default order, which must be
// stable so it does not defeat provider-side prompt caching.
func TestToolsSortByNameWithoutConfiguration(t *testing.T) {
	r := New()
	if err := r.SetTools([]ToolSchema{{Name: "zeta"}, {Name: "alpha"}, {Name: "mu"}}, nil); err != nil {
		t.Fatalf("SetTools: %v", err)
	}
	as, _ := r.Assemble(AssembleContext{})
	got := toolNames(as.Tools)
	want := []string{"alpha", "mu", "zeta"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

// TestToolOrderInsertsUnlistedAtTheRestMarker pins the configured order and the
// rest marker's role.
func TestToolOrderInsertsUnlistedAtTheRestMarker(t *testing.T) {
	r := New()
	if err := r.SetTools([]ToolSchema{{Name: "c"}, {Name: "a"}, {Name: "b"}}, nil); err != nil {
		t.Fatalf("SetTools: %v", err)
	}
	if err := r.SetToolOrder([]string{"c", ToolOrderRest, "a"}); err != nil {
		t.Fatalf("SetToolOrder: %v", err)
	}
	as, _ := r.Assemble(AssembleContext{})
	got := toolNames(as.Tools)
	// c first, then the unlisted tool (b) at the rest marker, then a.
	want := []string{"c", "b", "a"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

// TestToolOrderRequiresRestMarker pins that omitting the marker is refused:
// without it there is no defined place for an omitted tool, so the order would
// silently drop it.
func TestToolOrderRequiresRestMarker(t *testing.T) {
	r := New()
	err := r.SetToolOrder([]string{"a", "b"})
	if err == nil {
		t.Fatal("an order without the rest marker was accepted")
	}
	if !strings.Contains(err.Error(), ToolOrderRest) {
		t.Fatalf("error = %v, want it to name the marker", err)
	}
}

// TestToolOrderRejectsDuplicateNames pins the duplicate check.
func TestToolOrderRejectsDuplicateNames(t *testing.T) {
	r := New()
	if err := r.SetToolOrder([]string{"a", "a", ToolOrderRest}); err == nil {
		t.Fatal("a duplicate entry was accepted")
	}
}

// TestToolOrderRejectsUnknownNames pins that an order naming an unregistered
// tool fails, with the known set in the message.
func TestToolOrderRejectsUnknownNames(t *testing.T) {
	r := New()
	if err := r.SetTools([]ToolSchema{{Name: "known"}}, nil); err != nil {
		t.Fatalf("SetTools: %v", err)
	}
	err := r.SetToolOrder([]string{"ghost", ToolOrderRest})
	if err == nil {
		t.Fatal("an unregistered tool name was accepted")
	}
	if !strings.Contains(err.Error(), "ghost") || !strings.Contains(err.Error(), "known") {
		t.Fatalf("error = %v, want it to name both the unknown and the known tools", err)
	}
}

// TestToolOrderAcceptsARestrictedKnownName pins the two-universe rule: a name
// known but absent from THIS assembly still validates, because a restriction
// removed it rather than the tool not existing.
func TestToolOrderAcceptsARestrictedKnownName(t *testing.T) {
	r := New()
	// `hidden` is known but not present in the schemas.
	if err := r.SetTools([]ToolSchema{{Name: "visible"}}, []string{"visible", "hidden"}); err != nil {
		t.Fatalf("SetTools: %v", err)
	}
	if err := r.SetToolOrder([]string{"hidden", ToolOrderRest}); err != nil {
		t.Fatalf("a known-but-restricted name was refused: %v", err)
	}
}

// TestReservedToolNameRefused pins that a provider cannot return the marker as
// a real tool name.
func TestReservedToolNameRefused(t *testing.T) {
	r := New()
	if err := r.SetTools([]ToolSchema{{Name: ToolOrderRest}}, nil); err == nil {
		t.Fatal("a tool named with the reserved marker was accepted")
	}
}

// TestInvalidVariableNameRefusedAtRegistration pins early validation: a name
// that can never be referenced is a bug worth catching now.
func TestInvalidVariableNameRefusedAtRegistration(t *testing.T) {
	r := New()
	for _, name := range []string{"", "Bad", "1bad", "has-dash", "has space"} {
		if err := r.Variable(name, func(AssembleContext) string { return "" }); err == nil {
			t.Fatalf("Variable(%q) was accepted", name)
		}
	}
	if err := r.Variable("good_name_2", func(AssembleContext) string { return "v" }); err != nil {
		t.Fatalf("a valid name was refused: %v", err)
	}
}

// TestSectionOrdersTableMatchesOfficial pins a few values, so an accidental
// edit to the ordering contract is caught.
func TestSectionOrdersTableMatchesOfficial(t *testing.T) {
	cases := map[string]int{
		"HARNESS_IDENTITY":          -1000,
		"DEPLOYMENT_PERSONA_PREFIX": 0,
		"TOOL_BASH":                 1000,
		"DEPLOYMENT_PERSONA_SUFFIX": 10200,
	}
	for name, want := range cases {
		if got := SectionOrders[name]; got != want {
			t.Errorf("SectionOrders[%s] = %d, want %d", name, got, want)
		}
	}
	if got := ContextOrders["WORKING_DIRECTORY"]; got != 100 {
		t.Errorf("ContextOrders[WORKING_DIRECTORY] = %d, want 100", got)
	}
	if len(SectionOrders) != 32 {
		t.Errorf("SectionOrders has %d entries, want 32", len(SectionOrders))
	}
}

func toolNames(tools []ToolSchema) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	return out
}
