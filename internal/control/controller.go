// Package control is the transport-agnostic session driver. A Controller owns
// the agent run loop and session lifecycle, takes commands (Send/Cancel/Approve/
// SetPlanMode/Compact/NewSession/…), and emits everything that happens —
// reasoning, tool calls, approvals, turn completion — as a typed event stream to
// a single event.Sink.
//
// The point is one orchestration layer behind every frontend: a terminal TUI, a
// desktop webview, or an HTTP/SSE server each drive the Controller identically
// (issue commands, render events) and none of them re-implement turn lifecycle,
// cancellation, or approval. The Controller depends on no frontend.
package control

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"

	"reasonix/internal/agent"
	"reasonix/internal/command"
	"reasonix/internal/event"
	"reasonix/internal/permission"
	"reasonix/internal/plugin"
	"reasonix/internal/provider"
)

// Controller drives one chat session. Construct with New; drive with the command
// methods; observe through the Sink passed in Options.
type Controller struct {
	runner   agent.Runner
	executor *agent.Agent
	sink     event.Sink
	policy   permission.Policy

	label        string
	systemPrompt string
	sessionDir   string
	host         *plugin.Host
	commands     []command.Command
	cleanup      func()

	// promptMu serialises approval prompts so at most one is outstanding at a
	// time (parallel read-only tool calls don't normally gate, writers run
	// serially — but this keeps the contract explicit). Held across the blocking
	// wait, so it must never be taken by the Approve command path.
	promptMu sync.Mutex

	// mu guards the run state and approval bookkeeping; every critical section
	// under it is short and non-blocking.
	mu          sync.Mutex
	cancel      context.CancelFunc
	running     bool
	sessionPath string
	approvals   map[string]chan approvalReply
	granted     map[string]bool
	nextID      int
}

type approvalReply struct {
	allow   bool
	session bool
}

// Options carries the already-built pieces setup assembles. Lifecycle metadata
// lets the controller mint and rotate session files; Host/Commands are surfaced
// to frontends that resolve MCP prompts and slash commands.
type Options struct {
	Runner       agent.Runner
	Executor     *agent.Agent
	Sink         event.Sink
	Policy       permission.Policy
	Label        string
	SystemPrompt string
	SessionDir   string
	SessionPath  string
	Host         *plugin.Host
	Commands     []command.Command
	Cleanup      func()
}

// New builds a Controller. A nil Sink is replaced with event.Discard.
func New(opts Options) *Controller {
	sink := opts.Sink
	if sink == nil {
		sink = event.Discard
	}
	return &Controller{
		runner:       opts.Runner,
		executor:     opts.Executor,
		sink:         sink,
		policy:       opts.Policy,
		label:        opts.Label,
		systemPrompt: opts.SystemPrompt,
		sessionDir:   opts.SessionDir,
		sessionPath:  opts.SessionPath,
		host:         opts.Host,
		commands:     opts.Commands,
		cleanup:      opts.Cleanup,
		approvals:    map[string]chan approvalReply{},
		granted:      map[string]bool{},
	}
}

// --- commands (frontend → controller) ---

// Send starts a turn: it runs the agent on a background goroutine, streaming
// events to the Sink and finishing with a TurnDone event (Err set on failure;
// nil also for a user Cancel). A no-op if a turn is already in flight.
func (c *Controller) Send(input string) {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.running = true
	c.mu.Unlock()

	go func() {
		err := c.runner.Run(ctx, input)
		c.mu.Lock()
		c.running = false
		c.cancel = nil
		c.mu.Unlock()
		c.sink.Emit(event.Event{Kind: event.TurnDone, Err: err})
	}()
}

// Run executes a turn synchronously, returning the agent's error. Used by the
// headless `reasonix run` path, where the Sink renders to stdout and the caller
// just needs the exit status — no TurnDone event, no cancel bookkeeping.
func (c *Controller) Run(ctx context.Context, input string) error {
	return c.runner.Run(ctx, input)
}

// Cancel aborts the in-flight turn. A goroutine blocked awaiting approval
// unblocks via the cancelled context.
func (c *Controller) Cancel() {
	c.mu.Lock()
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Running reports whether a turn is currently in flight.
func (c *Controller) Running() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
}

// Approve answers a pending ApprovalRequest by ID: allow runs the call, session
// also remembers a grant for the rest of the session so the same tool+subject
// isn't re-prompted. Unknown/expired IDs are ignored.
func (c *Controller) Approve(id string, allow, session bool) {
	c.mu.Lock()
	reply := c.approvals[id]
	delete(c.approvals, id)
	c.mu.Unlock()
	if reply != nil {
		reply <- approvalReply{allow: allow, session: session} // buffered, never blocks
	}
}

// EnableInteractiveApproval swaps the executor's gate for one that routes "ask"
// decisions to the frontend via ApprovalRequest events. Interactive frontends
// (chat, desktop) call this; the headless run keeps the silent gate from setup.
func (c *Controller) EnableInteractiveApproval() {
	if c.executor != nil {
		c.executor.SetGate(permission.NewGate(c.policy, gateApprover{c}))
	}
}

// SetPlanMode flips the executor's read-only gate without touching the
// cache-stable prompt prefix.
func (c *Controller) SetPlanMode(v bool) {
	if c.executor != nil {
		c.executor.SetPlanMode(v)
	}
}

// Compact runs one compaction pass on the executor's session on demand.
func (c *Controller) Compact(ctx context.Context) error {
	if c.executor == nil {
		return nil
	}
	return c.executor.CompactNow(ctx)
}

// NewSession snapshots the current conversation, rotates to a fresh file, and
// resets the executor to a clean session carrying the same system prompt.
func (c *Controller) NewSession() error {
	if c.executor == nil {
		return nil
	}
	if err := c.Snapshot(); err != nil {
		return err
	}
	if c.sessionDir != "" {
		c.mu.Lock()
		c.sessionPath = agent.NewSessionPath(c.sessionDir, c.label)
		c.mu.Unlock()
	}
	c.executor.SetSession(agent.NewSession(c.systemPrompt))
	return nil
}

// Resume seeds the session from a loaded transcript and pins the active file to
// its path so auto-save keeps appending there.
func (c *Controller) Resume(s *agent.Session, path string) {
	if c.executor != nil {
		c.executor.SetSession(s)
	}
	c.mu.Lock()
	c.sessionPath = path
	c.mu.Unlock()
}

// Snapshot writes the executor's conversation to the active session file. No-op
// when persistence is unavailable. Called after every turn so a crash loses at
// most one in-flight prompt.
func (c *Controller) Snapshot() error {
	c.mu.Lock()
	path := c.sessionPath
	c.mu.Unlock()
	if c.executor == nil || path == "" {
		return nil
	}
	return c.executor.Session().Save(path)
}

// SetSessionPath pins where auto-save lands (a fresh session file minted by the
// caller when no resume path applies).
func (c *Controller) SetSessionPath(p string) {
	c.mu.Lock()
	c.sessionPath = p
	c.mu.Unlock()
}

// SessionDir reports the directory new session files land in ("" disables
// persistence), so the caller can decide whether to mint a path.
func (c *Controller) SessionDir() string { return c.sessionDir }

// History returns the executor's current message log (for repopulating a
// resumed frontend's view).
func (c *Controller) History() []provider.Message {
	if c.executor == nil {
		return nil
	}
	return c.executor.Session().Messages
}

// ContextSnapshot returns (promptTokens, contextWindow) from the most recent
// turn. Both zero means no data yet — a gauge hides itself.
func (c *Controller) ContextSnapshot() (int, int) {
	if c.executor == nil {
		return 0, 0
	}
	u := c.executor.LastUsage()
	if u == nil {
		return 0, c.executor.ContextWindow()
	}
	return u.PromptTokens, c.executor.ContextWindow()
}

// Host returns the running MCP host (nil when no plugins), for frontends that
// list servers / resolve MCP prompts.
func (c *Controller) Host() *plugin.Host { return c.host }

// Commands returns the loaded custom slash commands.
func (c *Controller) Commands() []command.Command { return c.commands }

// Label returns the human-readable model label, e.g. "deepseek-flash".
func (c *Controller) Label() string { return c.label }

// Close stops plugin subprocesses and releases resources.
func (c *Controller) Close() {
	if c.cleanup != nil {
		c.cleanup()
	}
}

// --- approval bridge (agent gate → events) ---

// gateApprover adapts the Controller to permission.Approver. It is distinct
// from the public Approve command (different signature, different direction).
type gateApprover struct{ c *Controller }

func (g gateApprover) Approve(ctx context.Context, tool, subject string, args json.RawMessage) (bool, bool, error) {
	return g.c.requestApproval(ctx, tool, subject)
}

// requestApproval emits an ApprovalRequest and blocks until Approve(ID, …)
// answers or ctx is cancelled. A prior session grant for the same tool+subject
// short-circuits. promptMu serialises outstanding prompts.
func (c *Controller) requestApproval(ctx context.Context, tool, subject string) (bool, bool, error) {
	key := tool + "\x00" + subject

	c.mu.Lock()
	if c.granted[key] {
		c.mu.Unlock()
		return true, false, nil
	}
	c.mu.Unlock()

	c.promptMu.Lock()
	defer c.promptMu.Unlock()

	// Re-check the grant: a session grant may have landed while we queued behind
	// another prompt for the same subject.
	c.mu.Lock()
	if c.granted[key] {
		c.mu.Unlock()
		return true, false, nil
	}
	c.nextID++
	id := strconv.Itoa(c.nextID)
	reply := make(chan approvalReply, 1)
	c.approvals[id] = reply
	c.mu.Unlock()

	c.sink.Emit(event.Event{Kind: event.ApprovalRequest, Approval: event.Approval{ID: id, Tool: tool, Subject: subject}})

	select {
	case r := <-reply:
		if r.allow && r.session {
			c.mu.Lock()
			c.granted[key] = true
			c.mu.Unlock()
		}
		// remember=false: session grants live here, not in the on-disk policy.
		return r.allow, false, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.approvals, id)
		c.mu.Unlock()
		return false, false, ctx.Err()
	}
}
