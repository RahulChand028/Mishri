package governance

import (
	"context"
	"testing"
)

func TestDefaultPolicyEngine_Evaluate(t *testing.T) {
	engine := NewDefaultPolicyEngine()
	ctx := context.Background()

	// Test Allow (Default)
	req1 := Request{Tool: "search"}
	res1, err := engine.Evaluate(ctx, req1)
	if err != nil {
		t.Fatalf("Evaluate failed: %v", err)
	}
	if res1.Effect != EffectAllow {
		t.Errorf("Expected EffectAllow, got %s", res1.Effect)
	}

	// Test Deny
	engine.DenyTool("shell")
	req2 := Request{Tool: "shell"}
	res2, err := engine.Evaluate(ctx, req2)
	if err != nil {
		t.Fatalf("Evaluate failed: %v", err)
	}
	if res2.Effect != EffectDeny {
		t.Errorf("Expected EffectDeny, got %s", res2.Effect)
	}
}
