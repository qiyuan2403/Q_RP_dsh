// Package tools registers the tools a model may call.
//
// A tool is a named function with a JSON Schema for its arguments. The registry
// holds them by name and hands the model their specs; the agent loop dispatches
// calls through it.
//
// Argument validation is not optional here. A model can emit any JSON, and a
// tool that assumes a well-formed input turns a model mistake into a crash, so
// a call whose arguments do not match the declared schema is refused before the
// tool runs.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/qiyuan2403/Q_RP_dsh/llm"
)

// Errors the registry reports.
var (
	// ErrDuplicate reports a name registered twice.
	ErrDuplicate = errors.New("tools: name is already registered")
	// ErrInvalidArgs reports arguments that do not match the tool's schema.
	ErrInvalidArgs = errors.New("tools: invalid arguments")
)

// Tool is one callable capability.
type Tool struct {
	// Name is the identifier the model uses.
	Name string
	// Description tells the model when to use it.
	Description string
	// Parameters is a JSON Schema object describing the arguments.
	Parameters json.RawMessage
	// Run performs the call. A returned error becomes the tool result the model
	// sees, so a failure is reported to the model rather than aborting the turn.
	Run func(ctx context.Context, args json.RawMessage) (any, error)
}

// Registry holds the available tools.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}}
}

// Register adds a tool.
//
// A duplicate name is refused rather than replacing: a silent replacement would
// change what an existing conversation's tool calls resolve to.
func (r *Registry) Register(t Tool) error {
	if strings.TrimSpace(t.Name) == "" {
		return errors.New("tools: name is required")
	}
	if t.Run == nil {
		return errors.New("tools: run is required")
	}
	if len(t.Parameters) > 0 && !json.Valid(t.Parameters) {
		return fmt.Errorf("tools: %s parameters are not valid JSON", t.Name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[t.Name]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicate, t.Name)
	}
	if len(t.Parameters) == 0 {
		t.Parameters = json.RawMessage(`{"type":"object","properties":{}}`)
	}
	r.tools[t.Name] = t
	return nil
}

// Lookup returns a tool by name.
func (r *Registry) Lookup(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// Specs returns the tool specs for a model request, ordered by name.
//
// The order is stable so a request's tool list does not change between calls:
// an unstable order defeats provider-side prompt caching.
func (r *Registry) Specs() []llm.ToolSpec {
	r.mu.RLock()
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	r.mu.RUnlock()
	sort.Strings(names)

	out := make([]llm.ToolSpec, 0, len(names))
	for _, name := range names {
		r.mu.RLock()
		t := r.tools[name]
		r.mu.RUnlock()
		out = append(out, llm.ToolSpec{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.Parameters,
		})
	}
	return out
}

// Names returns the registered tool names, ordered.
func (r *Registry) Names() []string {
	specs := r.Specs()
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.Name)
	}
	return out
}

// Validate checks arguments against a tool's declared schema.
//
// The check covers what a model actually gets wrong: a missing required
// property, an argument that is not an object, and a value whose JSON type
// contradicts the schema. It is not a full JSON Schema implementation, and it
// does not pretend to be — a schema feature outside this set is ignored rather
// than guessed at.
func Validate(t Tool, args json.RawMessage) error {
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	var schema struct {
		Type       string                    `json:"type"`
		Properties map[string]map[string]any `json:"properties"`
		Required   []string                  `json:"required"`
	}
	if err := json.Unmarshal(t.Parameters, &schema); err != nil {
		return fmt.Errorf("tools: %s has an unreadable schema: %w", t.Name, err)
	}

	var value any
	if err := json.Unmarshal(args, &value); err != nil {
		return fmt.Errorf("%w: %s arguments are not valid JSON: %v", ErrInvalidArgs, t.Name, err)
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: %s arguments must be an object, got %T", ErrInvalidArgs, t.Name, value)
	}

	for _, name := range schema.Required {
		if _, present := obj[name]; !present {
			return fmt.Errorf("%w: %s is missing required argument %q", ErrInvalidArgs, t.Name, name)
		}
	}

	for name, propSchema := range schema.Properties {
		raw, present := obj[name]
		if !present {
			continue
		}
		want, _ := propSchema["type"].(string)
		if want == "" {
			continue
		}
		if !typeMatches(want, raw) {
			return fmt.Errorf("%w: %s argument %q must be %s, got %s",
				ErrInvalidArgs, t.Name, name, want, jsonTypeName(raw))
		}
	}
	return nil
}

// typeMatches reports whether a decoded JSON value satisfies a schema type.
func typeMatches(want string, v any) bool {
	switch want {
	case "string":
		_, ok := v.(string)
		return ok
	case "number":
		_, ok := v.(float64)
		return ok
	case "integer":
		f, ok := v.(float64)
		return ok && f == float64(int64(f))
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "null":
		return v == nil
	default:
		// An unrecognized type name is not this validator's business.
		return true
	}
}

func jsonTypeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return fmt.Sprintf("%T", v)
	}
}
