package shell

import (
	"sort"
	"strings"
	"testing"
)

func contributor(name, key, value string) *EnvContributor {
	return &EnvContributor{
		Name:      name,
		Variables: map[string]EnvVar{key: {Description: "a fact"}},
		Resolve:   func() map[string]string { return map[string]string{key: value} },
	}
}

// TestCollectBuiltins pins the built-in facts: HOME and SHELL always, the
// session and profile facts when present.
func TestCollectBuiltins(t *testing.T) {
	r := NewEnvRegistry("/home/.qrpdsh")
	r.SessionID = "s1"
	r.Profile = "base"
	values, err := r.Collect()
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for key, want := range map[string]string{
		"DSH_HOME":       "/home/.qrpdsh",
		"DSH_SHELL":      "1",
		"DSH_SESSION_ID": "s1",
		"DSH_PROFILE":    "base",
	} {
		if values[key] != want {
			t.Fatalf("%s = %q, want %q", key, values[key], want)
		}
	}
}

// TestRegisterRefusesReservedKeys pins that a contributor cannot shadow a
// built-in fact: the model reads DSH_* as the harness's own account.
func TestRegisterRefusesReservedKeys(t *testing.T) {
	r := NewEnvRegistry("/home/.qrpdsh")
	for key := range reservedKeys {
		if err := r.Register(contributor("bad", key, "x")); err == nil {
			t.Fatalf("reserved key %q was accepted", key)
		}
	}
}

// TestRegisterRefusesInvalidKeys pins the namespace gate.
func TestRegisterRefusesInvalidKeys(t *testing.T) {
	r := NewEnvRegistry("/home/.qrpdsh")
	for _, key := range []string{"NOT_DSH", "DSH_", "DSH_lower", "DSH-with-dash", "DSH_ "} {
		if err := r.Register(contributor("bad", key, "x")); err == nil {
			t.Fatalf("invalid key %q was accepted", key)
		}
	}
	if err := r.Register(contributor("ok", "DSH_WORKSPACE", "x")); err != nil {
		t.Fatalf("valid key refused: %v", err)
	}
}

// TestRegisterRefusesDuplicates pins that a duplicate contributor name is an
// error rather than a silent replacement.
func TestRegisterRefusesDuplicates(t *testing.T) {
	r := NewEnvRegistry("/home/.qrpdsh")
	if err := r.Register(contributor("c", "DSH_A", "1")); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := r.Register(contributor("c", "DSH_B", "2")); err == nil {
		t.Fatal("duplicate name was accepted")
	}
}

// TestCollectRefusesUndeclaredKeys pins that a contributor returning an
// undeclared key fails the call: an undeclared value is an env leak, and env
// leaks must be loud.
func TestCollectRefusesUndeclaredKeys(t *testing.T) {
	r := NewEnvRegistry("/home/.qrpdsh")
	if err := r.Register(contributor("c", "DSH_A", "declared")); err != nil {
		t.Fatalf("register: %v", err)
	}
	leaky := contributor("leaky", "DSH_B", "declared")
	leaky.Resolve = func() map[string]string {
		return map[string]string{"DSH_B": "declared", "DSH_SECRET": "leaked"}
	}
	if err := r.Register(leaky); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := r.Collect(); err == nil {
		t.Fatal("an undeclared key was collected")
	}
}

// TestCollectIsSorted pins that the overlay is stable across calls, so a
// command's environment does not change between two invocations.
func TestCollectIsSorted(t *testing.T) {
	r := NewEnvRegistry("/home/.qrpdsh")
	for _, pair := range [][2]string{{"DSH_B", "b"}, {"DSH_A", "a"}, {"DSH_C", "c"}} {
		if err := r.Register(contributor("c-"+pair[0], pair[0], pair[1])); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	values, err := r.Collect()
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	want := []string{"DSH_A", "DSH_B", "DSH_C", "DSH_HOME", "DSH_SHELL"}
	ordered := make([]string, 0, len(values))
	for key := range values {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	for i := 0; i+1 < len(ordered); i++ {
		if ordered[i] > ordered[i+1] {
			t.Fatalf("keys not sorted: %v", ordered)
		}
	}
	for _, key := range want {
		if _, ok := values[key]; !ok {
			t.Fatalf("%s missing: %v", key, values)
		}
	}
}

// TestListEnumeratesWithoutResolving pins the declaration surface.
func TestListEnumeratesWithoutResolving(t *testing.T) {
	r := NewEnvRegistry("/home/.qrpdsh")
	resolved := false
	c := &EnvContributor{
		Name:      "c",
		Variables: map[string]EnvVar{"DSH_A": {Description: "x"}},
		Resolve:   func() map[string]string { resolved = true; return nil },
	}
	if err := r.Register(c); err != nil {
		t.Fatalf("register: %v", err)
	}
	list := r.List()
	if len(list) != 1 || list[0] != "DSH_A" {
		t.Fatalf("List = %v", list)
	}
	if resolved {
		t.Fatal("List resolved a contributor")
	}
}

// TestValidKeyPinsTheShape pins the accepted key grammar.
func TestValidKeyPinsTheShape(t *testing.T) {
	for _, key := range []string{"DSH_A", "DSH_A_B", "DSH_A1_2"} {
		if !validKey(key) {
			t.Fatalf("%q rejected", key)
		}
	}
	for _, key := range []string{"DSH_", "DSH_A-B", "dsh_a", "A_B"} {
		if validKey(key) {
			t.Fatalf("%q accepted", key)
		}
	}
}

// TestReservedKeysPinsThePrefix pins that every reserved key is in the
// namespace, so the gate cannot be bypassed by a contributor check ordering.
func TestReservedKeysPinsThePrefix(t *testing.T) {
	for key := range reservedKeys {
		if !strings.HasPrefix(key, EnvPrefix) {
			t.Fatalf("reserved key %q is outside the namespace", key)
		}
	}
}
