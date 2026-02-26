package governance

import (
	"context"
	"fmt"
	"regexp"
)

// Effect defines the result of a policy evaluation.
type Effect string

const (
	EffectAllow Effect = "allow"
	EffectDeny  Effect = "deny"
)

// Request contains the context of a tool call to be evaluated.
type Request struct {
	Tool      string
	Arguments string
	ChatID    string
}

// Result contains the outcome of a policy evaluation.
type Result struct {
	Effect Effect
	Reason string
}

// PolicyEngine evaluates tool calls against a set of rules.
type PolicyEngine interface {
	Evaluate(ctx context.Context, req Request) (Result, error)
}

// DefaultPolicyEngine is a basic implementation of PolicyEngine.
type DefaultPolicyEngine struct {
	DeniedTools map[string]bool
	DeniedRegex []*regexp.Regexp
}

func NewDefaultPolicyEngine() *DefaultPolicyEngine {
	return &DefaultPolicyEngine{
		DeniedTools: make(map[string]bool),
		DeniedRegex: make([]*regexp.Regexp, 0),
	}
}

func (e *DefaultPolicyEngine) DenyTool(name string) {
	e.DeniedTools[name] = true
}

func (e *DefaultPolicyEngine) DenyArguments(pattern string) error {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return err
	}
	e.DeniedRegex = append(e.DeniedRegex, re)
	return nil
}

func (e *DefaultPolicyEngine) Evaluate(ctx context.Context, req Request) (Result, error) {
	if e.DeniedTools[req.Tool] {
		return Result{
			Effect: EffectDeny,
			Reason: fmt.Sprintf("Tool '%s' is restricted by system policy", req.Tool),
		}, nil
	}

	for _, re := range e.DeniedRegex {
		if re.MatchString(req.Arguments) {
			return Result{
				Effect: EffectDeny,
				Reason: fmt.Sprintf("Arguments match restricted pattern: %s", re.String()),
			}, nil
		}
	}

	return Result{
		Effect: EffectAllow,
		Reason: "Approved by default policy",
	}, nil
}
