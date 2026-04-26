package permission

import "testing"

func TestEvaluator_Default_Ask(t *testing.T) {
	e := &Evaluator{}
	if got := e.Evaluate("bash", "anything"); got != DecisionAsk {
		t.Fatalf("expected ask, got %v", got)
	}
}

func TestEvaluator_AllowMatch(t *testing.T) {
	e := &Evaluator{Rules: []Rule{
		{Tool: "bash", Key: "ls *", Action: DecisionAllow},
	}}
	if e.Evaluate("bash", "ls -la") != DecisionAllow {
		t.Fatal("expected allow")
	}
}

func TestEvaluator_DenyMatch(t *testing.T) {
	e := &Evaluator{Rules: []Rule{
		{Tool: "bash", Key: "rm *", Action: DecisionDeny},
	}}
	if e.Evaluate("bash", "rm -rf /") != DecisionDeny {
		t.Fatal("expected deny")
	}
}

func TestEvaluator_LastWins(t *testing.T) {
	e := &Evaluator{Rules: []Rule{
		{Tool: "bash", Key: "*", Action: DecisionAllow},
		{Tool: "bash", Key: "rm *", Action: DecisionDeny},
	}}
	if e.Evaluate("bash", "rm -rf /") != DecisionDeny {
		t.Fatal("expected deny (last wins)")
	}
	if e.Evaluate("bash", "ls") != DecisionAllow {
		t.Fatal("expected allow (only first rule matches)")
	}
}

func TestEvaluator_LastWins_MoreSpecificFirst_StillLastWins(t *testing.T) {
	// Last-wins semantics: order of rules dictates precedence, NOT specificity.
	// The user is responsible for ordering most-specific deny LAST when needed.
	e := &Evaluator{Rules: []Rule{
		{Tool: "bash", Key: "rm *", Action: DecisionDeny},
		{Tool: "*", Key: "*", Action: DecisionAllow},
	}}
	if e.Evaluate("bash", "rm -rf /") != DecisionAllow {
		t.Fatal("the LAST matching rule (catch-all allow) should win, even though it's broader")
	}
}

func TestEvaluator_ToolWildcard(t *testing.T) {
	e := &Evaluator{Rules: []Rule{
		{Tool: "memory_*", Key: "*", Action: DecisionAllow},
	}}
	if e.Evaluate("memory_update", "anything") != DecisionAllow {
		t.Fatal("expected allow")
	}
	if e.Evaluate("memory_load", "anything") != DecisionAllow {
		t.Fatal("expected allow")
	}
	if e.Evaluate("bash", "anything") != DecisionAsk {
		t.Fatal("expected ask (no match)")
	}
}

func TestEvaluator_KeyEmptyMatchesEmptyPatternStar(t *testing.T) {
	e := &Evaluator{Rules: []Rule{
		{Tool: "*", Key: "*", Action: DecisionAllow},
	}}
	// Empty key + Key="*" → match
	if e.Evaluate("anytool", "") != DecisionAllow {
		t.Fatal("expected '*' to match empty key")
	}
}

func TestDecision_String(t *testing.T) {
	cases := map[Decision]string{
		DecisionAsk:   "ask",
		DecisionAllow: "allow",
		DecisionDeny:  "deny",
	}
	for d, want := range cases {
		if got := d.String(); got != want {
			t.Fatalf("Decision(%d).String()=%q want %q", d, got, want)
		}
	}
}

func TestEvaluator_ZeroValueIsValid(t *testing.T) {
	var e Evaluator
	if e.Evaluate("x", "y") != DecisionAsk {
		t.Fatal("expected ask")
	}
}
