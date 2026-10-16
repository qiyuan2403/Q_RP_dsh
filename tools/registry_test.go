package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func noop(context.Context, json.RawMessage) (any, error) { return "ok", nil }

// TestRegisterRefusesDuplicateName pins that a duplicate is refused rather than
// silently replacing: a replacement would change what an existing
// conversation's tool calls resolve to.
func TestRegisterRefusesDuplicateName(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Tool{Name: "a", Run: noop}); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := r.Register(Tool{Name: "a", Run: noop}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("error = %v, want ErrDuplicate", err)
	}
}

// TestRegisterRequiresNameAndRun pins both preconditions.
func TestRegisterRequiresNameAndRun(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Tool{Run: noop}); err == nil {
		t.Fatal("a tool without a name was accepted")
	}
	if err := r.Register(Tool{Name: "x"}); err == nil {
		t.Fatal("a tool without a run function was accepted")
	}
}

// TestRegisterRejectsInvalidSchema pins that a malformed schema is caught at
// registration rather than at the first model call.
func TestRegisterRejectsInvalidSchema(t *testing.T) {
	r := NewRegistry()
	err := r.Register(Tool{Name: "x", Parameters: json.RawMessage(`{not json`), Run: noop})
	if err == nil {
		t.Fatal("an invalid schema was accepted")
	}
}

// TestRegisterDefaultsSchema pins that a tool with no declared parameters gets
// an empty object schema, so the model still receives a valid definition.
func TestRegisterDefaultsSchema(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Tool{Name: "x", Run: noop}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	tool, _ := r.Lookup("x")
	var schema map[string]any
	if err := json.Unmarshal(tool.Parameters, &schema); err != nil {
		t.Fatalf("default schema is not JSON: %v", err)
	}
	if schema["type"] != "object" {
		t.Fatalf("default schema = %v", schema)
	}
}

// TestSpecsAreSortedByName pins a stable order: an unstable tool list defeats
// provider-side prompt caching.
func TestSpecsAreSortedByName(t *testing.T) {
	r := NewRegistry()
	for _, name := range []string{"zeta", "alpha", "mu"} {
		if err := r.Register(Tool{Name: name, Run: noop}); err != nil {
			t.Fatalf("Register(%s): %v", name, err)
		}
	}
	got := r.Names()
	want := []string{"alpha", "mu", "zeta"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

// TestValidateAcceptsWellFormedArguments pins the happy path.
func TestValidateAcceptsWellFormedArguments(t *testing.T) {
	tool := Tool{
		Name:       "t",
		Parameters: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"},"n":{"type":"integer"}},"required":["text"]}`),
	}
	if err := Validate(tool, json.RawMessage(`{"text":"hi","n":3}`)); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// TestValidateRejectsMissingRequired pins the most common model mistake.
func TestValidateRejectsMissingRequired(t *testing.T) {
	tool := Tool{
		Name:       "t",
		Parameters: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
	}
	err := Validate(tool, json.RawMessage(`{}`))
	if !errors.Is(err, ErrInvalidArgs) {
		t.Fatalf("error = %v, want ErrInvalidArgs", err)
	}
}

// TestValidateRejectsWrongType pins type checking.
func TestValidateRejectsWrongType(t *testing.T) {
	tool := Tool{
		Name:       "t",
		Parameters: json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer"}}}`),
	}
	if err := Validate(tool, json.RawMessage(`{"n":"not a number"}`)); !errors.Is(err, ErrInvalidArgs) {
		t.Fatalf("error = %v, want ErrInvalidArgs", err)
	}
	// A fractional value is not an integer.
	if err := Validate(tool, json.RawMessage(`{"n":1.5}`)); !errors.Is(err, ErrInvalidArgs) {
		t.Fatalf("error = %v, want a non-integer to be refused", err)
	}
	if err := Validate(tool, json.RawMessage(`{"n":2}`)); err != nil {
		t.Fatalf("a whole number was refused: %v", err)
	}
}

// TestValidateRejectsNonObjectArguments pins that a bare scalar is refused: a
// tool expects an argument object.
func TestValidateRejectsNonObjectArguments(t *testing.T) {
	tool := Tool{Name: "t", Parameters: json.RawMessage(`{"type":"object"}`)}
	if err := Validate(tool, json.RawMessage(`"a string"`)); !errors.Is(err, ErrInvalidArgs) {
		t.Fatalf("error = %v, want ErrInvalidArgs", err)
	}
}

// TestValidateTreatsEmptyArgumentsAsEmptyObject pins that an absent argument
// body is a valid no-argument call.
func TestValidateTreatsEmptyArgumentsAsEmptyObject(t *testing.T) {
	tool := Tool{Name: "t", Parameters: json.RawMessage(`{"type":"object","properties":{}}`)}
	if err := Validate(tool, nil); err != nil {
		t.Fatalf("Validate(nil): %v", err)
	}
}

// TestValidateIgnoresUnknownSchemaKeywords pins that an unrecognized type name
// is not this validator's business rather than a spurious refusal.
func TestValidateIgnoresUnknownSchemaKeywords(t *testing.T) {
	tool := Tool{
		Name:       "t",
		Parameters: json.RawMessage(`{"type":"object","properties":{"x":{"type":"some-future-type"}}}`),
	}
	if err := Validate(tool, json.RawMessage(`{"x":123}`)); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}
