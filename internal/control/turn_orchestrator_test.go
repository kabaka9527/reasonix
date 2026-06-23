package control

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/hook"
	"reasonix/internal/provider"
	"reasonix/internal/tool"
)

func TestTurnOrchestratorRunsForegroundUnit(t *testing.T) {
	runner := &fakeTurnRunner{}
	c := New(Options{Runner: runner})
	c.SetPlanMode(true)

	o := newTurnOrchestrator(c)
	if err := o.runTurnWithRawDisplay(context.Background(), "draft the plan", "draft the plan", ""); err != nil {
		t.Fatal(err)
	}

	if len(runner.inputs) != 1 {
		t.Fatalf("runner inputs = %d, want 1", len(runner.inputs))
	}
	if !strings.HasPrefix(runner.inputs[0], PlanModeMarker) {
		t.Fatalf("orchestrator should compose plan marker before running, got %q", runner.inputs[0])
	}
}

func TestTurnOrchestratorGoalContinuationRunsStopPerUnit(t *testing.T) {
	prov := &scriptedTurns{turns: [][]provider.Chunk{
		textTurn("Started.\n\n[goal:continue]"),
		textTurn("Finished.\n\n[goal:complete]"),
	}}
	ag := agent.New(prov, tool.NewRegistry(), agent.NewSession(""), agent.Options{}, event.Discard)
	var stopEvents int
	hooks := hook.NewRunner([]hook.ResolvedHook{{
		HookConfig: hook.HookConfig{Command: "record-stop"},
		Event:      hook.Stop,
		Scope:      hook.ScopeProject,
	}}, "", func(_ context.Context, in hook.SpawnInput) hook.SpawnResult {
		var p hook.Payload
		if err := json.Unmarshal([]byte(in.Stdin), &p); err != nil {
			t.Fatalf("hook payload: %v", err)
		}
		if p.Event == hook.Stop {
			stopEvents++
		}
		return hook.SpawnResult{ExitCode: 0}
	}, nil)
	c := New(Options{Runner: ag, Executor: ag, Hooks: hooks})
	c.SetGoal("ship the refactor")

	o := newTurnOrchestrator(c)
	if err := o.runGoalLoopWithRawDisplay(context.Background(), "Start pursuing the active goal now.", "ship the refactor", ""); err != nil {
		t.Fatal(err)
	}

	if prov.call != 2 {
		t.Fatalf("provider calls = %d, want initial + continuation", prov.call)
	}
	if stopEvents != 2 {
		t.Fatalf("Stop hook events = %d, want one per goal-loop turn unit", stopEvents)
	}
}

func TestTurnOrchestratorApprovedPlanSharesOneStopHook(t *testing.T) {
	prov := &scriptedTurns{turns: [][]provider.Chunk{
		textTurn("Plan:\n1. Make the change\n2. Verify it"),
		textTurn("Done."),
	}}
	ag := agent.New(prov, tool.NewRegistry(), agent.NewSession(""), agent.Options{}, event.Discard)
	approvalID := make(chan string, 1)
	var promptSubmitEvents, stopEvents int
	hooks := hook.NewRunner([]hook.ResolvedHook{
		{
			HookConfig: hook.HookConfig{Command: "record-submit"},
			Event:      hook.UserPromptSubmit,
			Scope:      hook.ScopeProject,
		},
		{
			HookConfig: hook.HookConfig{Command: "record-stop"},
			Event:      hook.Stop,
			Scope:      hook.ScopeProject,
		},
	}, "", func(_ context.Context, in hook.SpawnInput) hook.SpawnResult {
		var p hook.Payload
		if err := json.Unmarshal([]byte(in.Stdin), &p); err != nil {
			t.Fatalf("hook payload: %v", err)
		}
		switch p.Event {
		case hook.UserPromptSubmit:
			promptSubmitEvents++
		case hook.Stop:
			stopEvents++
		}
		return hook.SpawnResult{ExitCode: 0}
	}, nil)
	c := New(Options{
		Runner:   ag,
		Executor: ag,
		Hooks:    hooks,
		Sink: event.FuncSink(func(e event.Event) {
			if e.Kind == event.ApprovalRequest {
				approvalID <- e.Approval.ID
			}
		}),
	})
	c.SetPlanMode(true)
	go func() { c.Approve(<-approvalID, true, false, false) }()

	o := newTurnOrchestrator(c)
	if err := o.runTurnWithRawDisplay(context.Background(), "plan this change", "plan this change", ""); err != nil {
		t.Fatal(err)
	}

	if prov.call != 2 {
		t.Fatalf("provider calls = %d, want plan + approved execution", prov.call)
	}
	if promptSubmitEvents != 1 {
		t.Fatalf("UserPromptSubmit events = %d, want one for plan + approved execution unit", promptSubmitEvents)
	}
	if stopEvents != 1 {
		t.Fatalf("Stop hook events = %d, want one for plan + approved execution unit", stopEvents)
	}
}
