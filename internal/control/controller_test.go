package control

import (
	"context"
	"testing"

	"reasonix/internal/event"
)

// approvalIDs returns a Controller whose Sink forwards each ApprovalRequest's ID
// onto the channel, plus a counter of how many requests it emitted.
func approvalIDs() (*Controller, chan string, *int) {
	ids := make(chan string, 8)
	prompts := 0
	c := New(Options{Sink: event.FuncSink(func(e event.Event) {
		if e.Kind == event.ApprovalRequest {
			prompts++
			ids <- e.Approval.ID
		}
	})})
	return c, ids, &prompts
}

// TestApprovalAllowOnce drives the happy path: the gate emits an ApprovalRequest,
// the (fake) frontend answers allow, and the gate returns allow with no grant.
func TestApprovalAllowOnce(t *testing.T) {
	c, ids, _ := approvalIDs()
	go func() { c.Approve(<-ids, true, false) }()

	allow, remember, err := gateApprover{c}.Approve(context.Background(), "bash", "go test", nil)
	if err != nil || !allow || remember {
		t.Fatalf("Approve = (%v,%v,%v), want allow once", allow, remember, err)
	}
}

// TestApprovalDeny confirms a declined call returns allow=false.
func TestApprovalDeny(t *testing.T) {
	c, ids, _ := approvalIDs()
	go func() { c.Approve(<-ids, false, false) }()

	allow, _, err := gateApprover{c}.Approve(context.Background(), "bash", "rm -rf /", nil)
	if err != nil || allow {
		t.Fatalf("Approve = (%v,%v), want deny", allow, err)
	}
}

// TestApprovalSessionGrant proves an "allow this session" answer short-circuits
// later prompts for the same tool+subject: only the first reaches the frontend.
func TestApprovalSessionGrant(t *testing.T) {
	c, ids, prompts := approvalIDs()
	go func() {
		for id := range ids {
			c.Approve(id, true, true)
		}
	}()

	for i := 0; i < 3; i++ {
		allow, _, err := gateApprover{c}.Approve(context.Background(), "bash", "go build", nil)
		if err != nil || !allow {
			t.Fatalf("call %d = (%v,%v), want allow", i, allow, err)
		}
	}
	if *prompts != 1 {
		t.Errorf("prompted %d times, want 1 (session grant should short-circuit)", *prompts)
	}
}

// TestApprovalCtxCancel ensures a cancelled turn unblocks the gate with an error
// (rather than hanging) when no one answers.
func TestApprovalCtxCancel(t *testing.T) {
	c := New(Options{Sink: event.Discard})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	allow, _, err := gateApprover{c}.Approve(ctx, "bash", "x", nil)
	if err == nil || allow {
		t.Fatalf("Approve on cancelled ctx = (%v,%v), want (false, error)", allow, err)
	}
}
