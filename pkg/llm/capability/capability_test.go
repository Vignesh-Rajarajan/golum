package capability

import "testing"

func TestResolvePrefersMoreSpecific(t *testing.T) {
	r := &Registry{}
	if err := r.Add(Rule{
		Selector: Selector{Provider: "openai", Family: "gpt"},
		Caps:     Caps{ForcedToolChoice: Supported},
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(Rule{
		Selector: Selector{Provider: "openai", Family: "gpt", Model: "gpt-4o-mini"},
		Caps:     Caps{ForcedToolChoice: Unsupported},
	}); err != nil {
		t.Fatal(err)
	}
	got := r.Resolve(Selector{Provider: "openai", Model: "gpt-4o"})
	if got.ForcedToolChoice != Supported {
		t.Fatalf("gpt-4o = %s, want supported", got.ForcedToolChoice)
	}
	mini := r.Resolve(Selector{Provider: "openai", Model: "gpt-4o-mini"})
	if mini.ForcedToolChoice != Unsupported {
		t.Fatalf("gpt-4o-mini = %s, want unsupported", mini.ForcedToolChoice)
	}
}

func TestAddRejectsEqualSpecificityConflict(t *testing.T) {
	r := &Registry{}
	rule := Rule{
		Selector: Selector{Provider: "openai", Family: "gpt"},
		Caps:     Caps{ForcedToolChoice: Supported},
	}
	if err := r.Add(rule); err != nil {
		t.Fatal(err)
	}
	conflict := rule
	conflict.Caps.ForcedToolChoice = Unsupported
	if err := r.Add(conflict); err == nil {
		t.Fatal("expected a conflict error")
	}
}

func TestUnknownWhenNoRuleMatches(t *testing.T) {
	got := Default().Resolve(Selector{Provider: "openrouter", Model: "some/mystery"})
	if got.ForcedToolChoice != Unknown {
		t.Fatalf("got %s, want unknown", got.ForcedToolChoice)
	}
}

func TestDefaultOpenAIFamiliesSupportForcedToolChoice(t *testing.T) {
	r := Default()
	for _, model := range []string{"gpt-4o", "gpt-4.1", "o3-mini"} {
		got := r.Resolve(Selector{Provider: "openai", Model: model})
		if got.ForcedToolChoice != Supported {
			t.Fatalf("%s ForcedToolChoice=%s", model, got.ForcedToolChoice)
		}
	}
}

func TestFamilyOf(t *testing.T) {
	if FamilyOf("gpt-4o-mini") != "gpt" {
		t.Fatal(FamilyOf("gpt-4o-mini"))
	}
	if FamilyOf("o3-mini") != "o" {
		t.Fatal(FamilyOf("o3-mini"))
	}
}
