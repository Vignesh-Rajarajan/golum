package capability

import (
	"fmt"
	"strings"
)

// Level is a tri-state capability: unknown means "do not assume it works".
type Level int

const (
	Unknown Level = iota
	Supported
	Unsupported
)

func (l Level) String() string {
	switch l {
	case Supported:
		return "supported"
	case Unsupported:
		return "unsupported"
	default:
		return "unknown"
	}
}

// Caps is the structured knowledge the harness consults instead of scattering
// provider branches. ForcedToolChoice is the only field read today; the rest
// exist so a later quirk is one rule, not a new call site.
type Caps struct {
	ForcedToolChoice    Level
	ThinkingModes       Level
	DeveloperMessages   Level
	StrictSchemas       Level
	TokenCounting       Level
	UsageReporting      Level
	SystemPromptChanges Level
}

// Selector identifies a model at a given specificity. Empty fields are wildcards.
type Selector struct {
	Provider  string
	Family    string
	Model     string
	Transport string
}

// Rule binds a selector to capabilities.
type Rule struct {
	Selector Selector
	Caps     Caps
}

type conflictError struct{ a, b Rule }

func (e conflictError) Error() string {
	return fmt.Sprintf("conflicting capability rules of equal specificity: %+v vs %+v", e.a.Selector, e.b.Selector)
}

// Registry is an ordered set of rules. More-specific selectors win.
type Registry struct {
	rules []Rule
}

// Default returns the built-in compatibility table.
func Default() *Registry {
	r := &Registry{}
	_ = r.Add(Rule{
		Selector: Selector{Provider: "openai", Family: "gpt", Transport: "chat_completions"},
		Caps:     Caps{ForcedToolChoice: Supported},
	})
	_ = r.Add(Rule{
		Selector: Selector{Provider: "openai", Family: "o", Transport: "chat_completions"},
		Caps:     Caps{ForcedToolChoice: Supported},
	})
	return r
}

// Add rejects a rule that disagrees with an existing rule of the same specificity.
func (r *Registry) Add(rule Rule) error {
	if r == nil {
		return fmt.Errorf("nil capability registry")
	}
	rule.Selector = normalize(rule.Selector)
	spec := specificity(rule.Selector)
	for _, existing := range r.rules {
		if specificity(existing.Selector) != spec || !sameSelector(existing.Selector, rule.Selector) {
			continue
		}
		if existing.Caps != rule.Caps {
			return conflictError{a: existing, b: rule}
		}
		return nil
	}
	r.rules = append(r.rules, rule)
	return nil
}

// Resolve picks the most specific matching rule. No match is Unknown caps.
func (r *Registry) Resolve(sel Selector) Caps {
	if r == nil {
		return Caps{}
	}
	sel = normalize(sel)
	var best Rule
	bestSpec := -1
	for _, rule := range r.rules {
		if !matches(rule.Selector, sel) {
			continue
		}
		spec := specificity(rule.Selector)
		if spec > bestSpec {
			best, bestSpec = rule, spec
		}
	}
	if bestSpec < 0 {
		return Caps{}
	}
	return best.Caps
}

// FamilyOf extracts a coarse family from a model id: gpt-4o -> gpt, o3-mini -> o.
func FamilyOf(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return ""
	}
	if strings.HasPrefix(model, "gpt") {
		return "gpt"
	}
	if strings.HasPrefix(model, "o1") || strings.HasPrefix(model, "o3") || strings.HasPrefix(model, "o4") {
		return "o"
	}
	if i := strings.IndexAny(model, "-./:"); i > 0 {
		return model[:i]
	}
	return model
}

func normalize(s Selector) Selector {
	s.Provider = strings.ToLower(strings.TrimSpace(s.Provider))
	s.Family = strings.ToLower(strings.TrimSpace(s.Family))
	s.Model = strings.ToLower(strings.TrimSpace(s.Model))
	s.Transport = strings.ToLower(strings.TrimSpace(s.Transport))
	if s.Family == "" && s.Model != "" {
		s.Family = FamilyOf(s.Model)
	}
	if s.Transport == "" {
		s.Transport = "chat_completions"
	}
	return s
}

func specificity(s Selector) int {
	n := 0
	if s.Transport != "" {
		n++
	}
	if s.Provider != "" {
		n += 2
	}
	if s.Family != "" {
		n += 4
	}
	if s.Model != "" {
		n += 8
	}
	return n
}

func sameSelector(a, b Selector) bool {
	return a.Provider == b.Provider && a.Family == b.Family && a.Model == b.Model && a.Transport == b.Transport
}

func matches(rule, sel Selector) bool {
	if rule.Provider != "" && rule.Provider != sel.Provider {
		return false
	}
	if rule.Family != "" && rule.Family != sel.Family {
		return false
	}
	if rule.Model != "" && rule.Model != sel.Model {
		return false
	}
	if rule.Transport != "" && rule.Transport != sel.Transport {
		return false
	}
	return true
}
