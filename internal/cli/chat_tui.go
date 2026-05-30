package cli

import (
	"context"
	"fmt"
	"image/color"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"reasonix/internal/agent"
	"reasonix/internal/command"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/i18n"
	"reasonix/internal/plugin"
	"reasonix/internal/provider"
)

// chatTUI is a bubbletea Model that runs a chat session in the terminal's
// normal buffer (no alt-screen). Finalized output — user bubbles, tool dispatch
// lines, usage lines, reasoning, and the rendered assistant answer — is
// committed to the native scrollback via tea.Println, so the wheel, scrollbar,
// and copy all work like any CLI. The bubbletea-managed region is only the
// bottom — input box, status line, an optional approval/plan banner, and the
// autocomplete menu — and it is kept a stable height (it changes only on
// discrete user actions, never per streamed token) so the renderer commits
// scrollback cleanly without stranding the input box's border lines. This
// mirrors how Claude Code uses Ink's <Static> to freeze finished output into
// scrollback while re-rendering just the active prompt.
type chatTUI struct {
	ctrl    *control.Controller
	label   string
	missing string // missing-key warning surfaced once in the banner, "" when ready

	width  int
	height int

	input   textarea.Model
	spinner spinner.Model

	state    tuiState
	runStart time.Time
	elapsed  int

	// planMode mirrors the agent's read-only gate (Tab toggles it). The marker
	// rides in outgoing user messages so the cache-stable prompt prefix is left
	// untouched.
	planMode bool

	// turnAccumulator collects this turn's assistant free text so the plan-mode
	// path can tell whether the turn produced a substantive proposal.
	turnAccumulator *strings.Builder

	// pendingPlan is non-empty while a plan-mode proposal awaits Enter-approval.
	pendingPlan string

	// history is a resumed session's messages, committed to scrollback once on
	// the first WindowSizeMsg so a reopened chat shows its prior transcript.
	history []provider.Message

	// reasoning accumulates the in-progress thinking stream (dim); pending
	// accumulates the in-progress answer (raw markdown). They are committed to
	// scrollback (reasoning verbatim, answer markdown-rendered) when they
	// finalize — at a tool/usage boundary or turn end — not previewed live, so
	// the bottom region stays a stable height. pendingCommit queues finalized
	// lines so a single Update emits exactly one ordered tea.Println.
	reasoning     *strings.Builder
	pending       *strings.Builder
	pendingCommit *[]string
	renderer      *mdRenderer
	eventCh       chan event.Event
	started       bool // banner + resumed history committed once

	// pendingApproval holds the tool-call approval currently shown in the banner
	// (nil when none). While set, the controller's run goroutine is blocked
	// awaiting ctrl.Approve and key input is captured to answer it.
	pendingApproval *event.Approval

	// host is the running MCP servers (nil when no plugins). The TUI reads
	// prompts (slash commands), resources (@-references), and server status
	// (/mcp) from it.
	host *plugin.Host

	// commands are custom slash commands loaded from .reasonix/commands; each renders
	// its template with the typed args and sends the result as a turn.
	commands []command.Command

	// completion is the live autocomplete menu (slash commands; @-refs later).
	completion completion
}

type tuiState int

const (
	tuiIdle tuiState = iota
	tuiRunning
)

// agentEventMsg is one typed event from the agent's run loop.
type agentEventMsg event.Event

// elapsedTickMsg fires once a second while a turn runs, driving the "thinking
// Ns" counter in the status line.
type elapsedTickMsg struct{}

// promptResolvedMsg carries the result of fetching an MCP prompt (an async
// prompts/get). display is the command line echoed as the user bubble; sent is
// the rendered prompt text that becomes the model turn.
type promptResolvedMsg struct {
	display string
	sent    string
	err     error
}

// refsResolvedMsg carries the result of resolving the @references in a
// submitted line (async file reads / MCP resources/read).
type refsResolvedMsg struct {
	line  string
	block string
	errs  []string
}

// newChatTUI assembles the initial model. The controller has already been wired
// with an event sink that feeds eventCh; the TUI issues commands to it and
// renders the events it emits. Label, history, host, and commands are read from
// the controller, so a resumed session pre-populates scrollback.
func newChatTUI(ctrl *control.Controller, missing string, eventCh chan event.Event, termW int) chatTUI {
	ti := textarea.New()
	ti.Prompt = ""
	ti.CharLimit = 16384
	ti.SetHeight(1)
	ti.ShowLineNumbers = false
	// Use the real terminal cursor (not a styled virtual one) so View can place
	// it at the insertion point and IME candidate windows anchor to the input.
	ti.SetVirtualCursor(false)
	// Plain Enter submits (the chatTUI handler intercepts it), so the textarea's
	// own InsertNewline binding moves to Alt+Enter / Ctrl+J.
	ti.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("alt+enter", "ctrl+j"))
	ti.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("173"))

	commitBuf := []string{}
	return chatTUI{
		ctrl:            ctrl,
		label:           ctrl.Label(),
		missing:         missing,
		input:           ti,
		spinner:         sp,
		reasoning:       &strings.Builder{},
		pending:         &strings.Builder{},
		pendingCommit:   &commitBuf,
		turnAccumulator: &strings.Builder{},
		renderer:        newMarkdownRenderer(termW),
		eventCh:         eventCh,
		history:         ctrl.History(),
		host:            ctrl.Host(),
		commands:        ctrl.Commands(),
	}
}

// prompts returns the MCP prompts discovered at startup (nil when no plugins).
func (m *chatTUI) prompts() []plugin.Prompt {
	if m.host == nil {
		return nil
	}
	return m.host.Prompts()
}

// planModeMarker is prepended to every user message while plan mode is on. It
// is part of the user message (not the system prompt or tools), so the existing
// cache prefix stays valid.
const planModeMarker = "[Plan mode — read-only. Explore and propose; do not write files, edit, or run side-effecting bash. Read-only tools (read_file, ls, grep, glob, web_fetch, task) are available; writers are refused by the harness. End with a concrete plan the user can approve.]"

func (m chatTUI) Init() tea.Cmd {
	return tea.Batch(
		textarea.Blink,
		waitForAgentEvent(m.eventCh),
	)
}

func (m chatTUI) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.input.SetWidth(msg.Width - 4)
		m.renderer = newMarkdownRenderer(msg.Width)
		// Commit the banner — and a resumed session's transcript — to scrollback
		// once, now that the width is known.
		if !m.started {
			m.started = true
			var b strings.Builder
			b.WriteString(renderTUIBanner(m.label, m.missing, msg.Width))
			if len(m.history) > 0 {
				r := newMarkdownRenderer(msg.Width)
				for _, sec := range replaySectionsFor(m.history, msg.Width, r) {
					b.WriteString(sec)
				}
				m.history = nil
			}
			m.commitLine(strings.TrimRight(b.String(), "\n"))
		}

	case tea.KeyPressMsg:
		// A pending tool approval is modal: keystrokes answer it (y/a/n, Enter,
		// Esc) rather than reaching the input.
		if m.pendingApproval != nil {
			return m.handleApprovalKey(msg)
		}
		// While the autocomplete menu is open it captures navigation/accept keys
		// (↑/↓ move, Tab/Enter accept, Esc close); everything else falls through
		// to the textarea and re-filters the menu at the end of Update.
		if m.completion.active {
			switch msg.String() {
			case "up":
				m.moveCompletion(-1)
				return m, nil
			case "down":
				m.moveCompletion(1)
				return m, nil
			case "tab", "enter":
				m.acceptCompletion()
				return m, nil
			case "esc":
				m.completion = completion{}
				return m, nil
			}
		}
		switch msg.String() {
		case "esc":
			// "Back out" of the most specific in-progress state: cancel a turn,
			// turn plan mode off, or clear typed-but-unsent input. Scrollback is
			// the terminal's now, so there's no viewport to dismiss.
			switch {
			case m.state == tuiRunning:
				m.ctrl.Cancel()
			case m.pendingPlan != "":
				m.pendingPlan = ""
			case m.planMode:
				m.planMode = false
				m.ctrl.SetPlanMode(false)
			default:
				m.input.Reset()
			}
			return m, nil
		case "ctrl+c":
			if m.state == tuiRunning {
				m.ctrl.Cancel()
				return m, nil
			}
			return m, tea.Quit
		case "ctrl+d":
			return m, tea.Quit
		case "tab":
			if m.state == tuiRunning {
				break
			}
			m.planMode = !m.planMode
			m.ctrl.SetPlanMode(m.planMode)
			return m, nil
		case "enter":
			if m.state == tuiRunning {
				return m, nil // ignore Enter while a turn is in flight
			}
			line := strings.TrimSpace(m.input.Value())

			// Empty Enter while a plan is pending approves it and auto-sends a
			// brief "proceed" message.
			if line == "" && m.pendingPlan != "" {
				m.pendingPlan = ""
				m.planMode = false
				m.ctrl.SetPlanMode(false)
				cmds = append(cmds, m.startTurn("Plan approved — proceed with the steps you laid out, executing as needed.", "(plan approved — executing)"))
				return m, finalize(m, cmds)
			}

			if line == "" {
				return m, nil
			}
			if line == "exit" || line == "quit" || line == ":q" {
				return m, tea.Quit
			}

			// Slash commands run locally without going through the model.
			if strings.HasPrefix(line, "/") {
				m.input.Reset()
				m.input.SetHeight(1)
				cmds = append(cmds, m.runSlashCommand(line))
				return m, finalize(m, cmds)
			}

			// A non-empty submission while a plan is pending counts as feedback.
			m.pendingPlan = ""
			m.input.Reset()
			m.input.SetHeight(1)

			// @references (local files / MCP resources) are fetched off the event
			// loop; the turn starts when they resolve (refsResolvedMsg).
			if refs := m.detectRefs(line); len(refs) > 0 {
				cmds = append(cmds, m.fetchRefs(line, refs))
				return m, finalize(m, cmds)
			}

			cmds = append(cmds, m.startTurn(m.composeSent(line), line))
			return m, finalize(m, cmds)
		}

	case agentEventMsg:
		m.ingestEvent(event.Event(msg))
		cmds = append(cmds, waitForAgentEvent(m.eventCh))

	case promptResolvedMsg:
		switch {
		case msg.err != nil:
			m.commitLine(wrapForViewport(i18n.M.ErrorPrefix+" "+msg.err.Error(), m.width, lipgloss.Color("3")))
		case strings.TrimSpace(msg.sent) == "":
			m.notice(i18n.M.SlashPromptEmpty)
		default:
			cmds = append(cmds, m.startTurn(m.composeSent(msg.sent), msg.display))
		}

	case refsResolvedMsg:
		for _, e := range msg.errs {
			m.notice(e) // surface a fetch failure but still send the turn
		}
		sent := msg.line
		if msg.block != "" {
			sent = "Referenced context:\n\n" + msg.block + "\n\n" + msg.line
		}
		cmds = append(cmds, m.startTurn(m.composeSent(sent), msg.line))

	case elapsedTickMsg:
		if m.state == tuiRunning {
			m.elapsed = int(time.Since(m.runStart).Seconds())
			cmds = append(cmds, elapsedTick())
		}

	case spinner.TickMsg:
		if m.state == tuiRunning {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			cmds = append(cmds, cmd)
		}
	}

	var ic tea.Cmd
	m.input, ic = m.input.Update(msg)
	cmds = append(cmds, ic)
	m.growInputToFit()
	// Re-filter the autocomplete menu against the freshly-edited input.
	if _, ok := msg.(tea.KeyPressMsg); ok {
		m.updateCompletion()
	}

	return m, finalize(m, cmds)
}

// finalize flushes any queued scrollback lines into a single ordered tea.Println
// (Batch doesn't preserve order across multiple Println cmds, so we coalesce per
// Update) and batches it with the turn's other commands.
func finalize(m chatTUI, cmds []tea.Cmd) tea.Cmd {
	if len(*m.pendingCommit) > 0 {
		out := strings.TrimRight(clampWidth(strings.Join(*m.pendingCommit, "\n"), m.width), "\n")
		*m.pendingCommit = (*m.pendingCommit)[:0]
		// Commit in screen-bounded chunks. v2's inline renderer commits scrollback
		// via insertAbove, which scrolls the screen and InsertLine()s by the
		// block's line count; a single block taller than the screen makes its
		// CursorUp clamp at the top and the inserts misalign — the whole frame
		// (input box, banner) corrupts. Splitting so each Println is at most a
		// screenful keeps insertAbove within bounds. Sequence preserves order
		// (Batch does not across multiple Printlns).
		var prints []tea.Cmd
		for _, chunk := range chunkLines(out, m.scrollChunkHeight()) {
			prints = append(prints, tea.Println(chunk))
		}
		cmds = append(cmds, tea.Sequence(prints...))
	}
	return tea.Batch(cmds...)
}

// scrollChunkHeight is the largest block (in lines) finalize prints at once so
// v2's insertAbove stays within the screen. It leaves room for the pinned
// bottom frame (input box + status). Falls back to a generous default before
// the first WindowSizeMsg sets the height.
func (m chatTUI) scrollChunkHeight() int {
	if m.height <= 0 {
		return 100
	}
	if n := m.height - 5; n > 1 {
		return n
	}
	return 1
}

// chunkLines splits s into blocks of at most n lines each, preserving order and
// line content. A single block is returned when it already fits.
func chunkLines(s string, n int) []string {
	if n < 1 {
		n = 1
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return []string{s}
	}
	var out []string
	for i := 0; i < len(lines); i += n {
		end := i + n
		if end > len(lines) {
			end = len(lines)
		}
		out = append(out, strings.Join(lines[i:end], "\n"))
	}
	return out
}

// clampWidth hard-breaks any line wider than width so no scrollback line wraps
// in the terminal. bubbletea's inline renderer estimates how far to scroll for
// each printed block from each line's width (insertAbove: offset += width/w); an
// over-wide line that the terminal wraps throws that estimate off and drifts the
// pinned input box off-screen. Lines already within width are left byte-for-byte
// untouched (chunkByWidth preserves content and ANSI), so rendered tables and the
// wrapped answer — which the markdown renderer already fit to width — are safe;
// only stray long lines (tool-dispatch args, unwrapped code) get broken.
func clampWidth(s string, width int) string {
	if width <= 0 {
		return s
	}
	var b strings.Builder
	for i, line := range strings.Split(s, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		if visibleWidth(line) > width {
			b.WriteString(strings.Join(chunkByWidth(line, width), "\n"))
		} else {
			b.WriteString(line)
		}
	}
	return b.String()
}

// commitLine queues one finalized block for the next scrollback flush.
func (m *chatTUI) commitLine(s string) {
	*m.pendingCommit = append(*m.pendingCommit, s)
}

// commitReasoning freezes the accumulated thinking stream (verbatim, already
// dim) into scrollback and clears the live buffer.
func (m *chatTUI) commitReasoning() {
	if m.reasoning.Len() == 0 {
		return
	}
	// Wrap to the viewport width before committing. bubbletea's non-alt-screen
	// Println adds an erase-to-end only for message lines *narrower* than the
	// terminal and never truncates them, so an over-wide reasoning line wraps
	// and its short final row leaves the old input-box border (the live region
	// it printed over) bleeding through on the right — the "ghost ────". The
	// rendered answer is already wrapped, which is why only reasoning stranded.
	// Wrap each over-long line; keep short ones (the "▎ thinking" header)
	// verbatim so their indent survives.
	raw := strings.TrimRight(m.reasoning.String(), "\n")
	var b strings.Builder
	for i, line := range strings.Split(raw, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		if m.width > 0 && visibleWidth(line) > m.width {
			b.WriteString(wrapAnsi(line, m.width))
		} else {
			b.WriteString(line) // width unknown (pre-sizing) or already fits: verbatim
		}
	}
	m.commitLine(b.String())
	m.reasoning.Reset()
}

// commitPending renders the accumulated answer as markdown and freezes it into
// scrollback. Joining commitReasoning then commitPending puts the answer on its
// own line, restoring the thinking→answer break the renderer strips.
func (m *chatTUI) commitPending() {
	if m.pending.Len() == 0 {
		return
	}
	raw := m.pending.String()
	rendered := m.renderer.Render(raw)
	if rendered == "" {
		rendered = raw
	}
	m.commitLine(strings.TrimRight(rendered, "\n"))
	m.pending.Reset()
}

// handleApprovalKey resolves a pending tool-call approval from a keystroke and
// re-arms the approval listener. y/Enter allows once, a allows for the rest of
// the session, n/Esc denies. Ctrl-C cancels the whole turn via the run context.
func (m chatTUI) handleApprovalKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	answer := func(allow, session bool) (tea.Model, tea.Cmd) {
		m.ctrl.Approve(m.pendingApproval.ID, allow, session)
		m.pendingApproval = nil
		return m, nil // the next ApprovalRequest / event arrives on eventCh
	}
	switch msg.String() {
	case "ctrl+c":
		m.ctrl.Cancel() // cancels the run; the approver unblocks via ctx.Done()
		return answer(false, false)
	case "enter":
		return answer(true, false)
	case "esc":
		return answer(false, false)
	}
	switch strings.ToLower(msg.String()) {
	case "y":
		return answer(true, false)
	case "a":
		return answer(true, true)
	case "n":
		return answer(false, false)
	}
	return m, nil // ignore anything else while awaiting a decision
}

var (
	// Input box: only top + bottom borders, no sides.
	inputBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), true, false, true, false).
			BorderForeground(lipgloss.Color("173")).
			PaddingLeft(1)

	// Approval banner: same frame as the input box, recoloured yellow.
	approvalBannerStyle = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder(), true, false, true, false).
				BorderForeground(lipgloss.Color("220")).
				Foreground(lipgloss.Color("220")).
				Bold(true).
				PaddingLeft(1)

	statusStyle = lipgloss.NewStyle().Faint(true)
)

func (m chatTUI) View() tea.View {
	boxW := m.width
	if boxW < 10 {
		boxW = 10
	}
	box := inputBoxStyle.Width(boxW).Render(m.input.View())

	var modeTag string
	if m.planMode {
		modeTag = yellow("[plan]")
	} else {
		modeTag = dim("[auto]")
	}

	ctxTag := m.contextTag()
	var status string
	switch {
	case m.pendingApproval != nil:
		status = "  " + modeTag + " · " + i18n.M.ChatStatusToolApproval
	case m.state == tuiRunning:
		status = fmt.Sprintf("  %s · "+i18n.M.ChatStatusThinkingFmt, modeTag, m.spinner.View(), m.elapsed)
	case m.pendingPlan != "":
		status = "  " + modeTag + " · " + i18n.M.ChatStatusPlanApproval
	default:
		status = "  " + modeTag + " · " + i18n.M.ChatStatusIdle
	}
	if ctxTag != "" {
		status += " · " + ctxTag
	}

	// The bottom region must stay a stable height: bubbletea's non-alt-screen
	// renderer commits scrollback via tea.Println by clearing the previous
	// frame's lines, so a frame whose height changed every streamed token (a
	// growing live preview) drifts and strands input-box border lines in the
	// history. So we don't preview the streaming text here — it lands in
	// scrollback at boundaries (tool lines stream live; reasoning and the
	// rendered answer commit at their edges). The menu/banner change height only
	// on discrete user actions, never mid-stream.
	var parts []string
	rowsAboveBox := 0 // terminal rows occupied by banner/menu before the input box
	if banner := m.renderApprovalBanner(); banner != "" {
		parts = append(parts, banner)
		rowsAboveBox += strings.Count(banner, "\n") + 1
	}
	if menu := m.renderCompletion(); menu != "" {
		parts = append(parts, menu)
		rowsAboveBox += strings.Count(menu, "\n") + 1
	}
	parts = append(parts, box, statusStyle.Render(status))

	v := tea.NewView(strings.Join(parts, "\n"))
	// Anchor the real terminal cursor at the textarea's insertion point so IME
	// candidate windows appear in the input box, not at the bottom of the frame.
	// input.Cursor() is relative to the textarea; offset it by the box's screen
	// position (rows above + the box's top border row; +1 column for PaddingLeft).
	if cur := m.input.Cursor(); cur != nil {
		cur.X += 1
		cur.Y += rowsAboveBox + 1
		v.Cursor = cur
	}
	return v
}

// contextTag renders the prompt-vs-context-window gauge for the status line.
func (m chatTUI) contextTag() string {
	used, window := m.ctrl.ContextSnapshot()
	if used == 0 || window == 0 {
		return ""
	}
	pct := used * 100 / window
	body := fmt.Sprintf("%s / %s ctx (%d%%)", shortTokens(used), shortTokens(window), pct)
	switch {
	case pct >= 85:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render(body)
	case pct >= 60:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("220")).Render(body)
	default:
		return dim(body)
	}
}

// shortTokens prints token counts compactly: 142_000 → "142K", 1_000_000 → "1M".
func shortTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%dK", n/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// renderApprovalBanner is the slim notice shown above the input while a tool
// call (or a plan) awaits the user's decision.
func (m chatTUI) renderApprovalBanner() string {
	w := m.width
	if w < 10 {
		w = 10
	}
	if m.pendingApproval != nil {
		subj := strings.TrimSpace(m.pendingApproval.Subject)
		if subj != "" {
			subj = " " + truncateSubject(subj, w)
		}
		text := fmt.Sprintf(i18n.M.ToolApprovalPromptFmt, m.pendingApproval.Tool, subj)
		return approvalBannerStyle.Width(w).Render("⏸ " + text)
	}
	if m.pendingPlan == "" {
		return ""
	}
	return approvalBannerStyle.Width(w).Render("⏸ " + i18n.M.PlanApprovalPrompt)
}

// truncateSubject trims a tool subject so the approval banner fits one line.
func truncateSubject(s string, width int) string {
	max := width - 28
	if max < 16 {
		max = 16
	}
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

// growInputToFit resizes the textarea to the number of lines its value spans,
// capped at maxInputRows so a long paste doesn't crowd the screen.
const maxInputRows = 5

func (m *chatTUI) growInputToFit() {
	lines := strings.Count(m.input.Value(), "\n") + 1
	if lines < 1 {
		lines = 1
	}
	if lines > maxInputRows {
		lines = maxInputRows
	}
	if lines != m.input.Height() {
		m.input.SetHeight(lines)
	}
}

// startTurn commits the user bubble to scrollback, resets the turn accumulator,
// and kicks off runner.Run. `sent` goes to the model (may carry a plan-mode
// marker); `displayed` is what the transcript shows.
func (m *chatTUI) startTurn(sent, displayed string) tea.Cmd {
	// Flush any half-streamed leftover before the new turn (defensive).
	m.commitReasoning()
	m.commitPending()
	m.turnAccumulator.Reset()
	m.commitLine("") // blank line separating turns
	m.commitLine(renderUserBubble(displayed, m.width, m.planMode))

	m.state = tuiRunning
	m.runStart = time.Now()
	m.elapsed = 0
	// The controller owns the run goroutine, its context, and cancellation; it
	// streams events to eventCh and emits TurnDone when the turn settles.
	m.ctrl.Send(sent)
	return tea.Batch(m.spinner.Tick, elapsedTick())
}

// ingestEvent routes one typed event from the agent. Reasoning (dim) and answer
// free-text accumulate in their live buffers; every other event first finalizes
// the reasoning and answer streamed so far, then commits its own line —
// preserving order. Switching on the event Kind replaces the old prefix-sniffing
// of a flattened byte stream: the structure is now explicit.
func (m *chatTUI) ingestEvent(e event.Event) {
	switch e.Kind {
	case event.Reasoning:
		if m.reasoning.Len() == 0 {
			m.reasoning.WriteString(dim("  ▎ thinking") + "\n")
		}
		m.reasoning.WriteString(dim(e.Text))

	case event.Text:
		m.commitReasoning() // reasoning ends as the answer begins
		m.pending.WriteString(e.Text)
		m.turnAccumulator.WriteString(e.Text)

	case event.Message:
		// The answer stream is complete — freeze reasoning + the markdown answer.
		m.commitReasoning()
		m.commitPending()

	case event.ToolDispatch:
		m.finalizeStreamed()
		m.commitLine(fmt.Sprintf("  -> %s %s", e.Tool.Name, compactArgs(e.Tool.Args)))

	case event.ToolResult:
		// A successful result is silent (it only feeds the model); a blocked call
		// surfaces a "⊘ name <reason>" line.
		if e.Tool.Err != "" {
			m.finalizeStreamed()
			m.commitLine(fmt.Sprintf("  ⊘ %s %s", e.Tool.Name, e.Tool.Err))
		}

	case event.Usage:
		if line := agent.FormatUsageLine(e.Usage, e.Pricing); line != "" {
			m.finalizeStreamed()
			m.commitLine(line)
		}

	case event.Notice:
		glyph := "·"
		if e.Level == event.LevelWarn {
			glyph = "!"
		}
		m.finalizeStreamed()
		m.commitLine(fmt.Sprintf("  %s %s", glyph, e.Text))

	case event.Phase:
		m.finalizeStreamed()
		m.commitLine(fmt.Sprintf("[%s]", e.Text))

	case event.ApprovalRequest:
		// The controller's run goroutine is now blocked inside the gate awaiting
		// this decision; the banner shows it in View and key input answers it via
		// ctrl.Approve. At most one prompt is outstanding (the controller
		// serialises them), so a plain field holds the current one.
		a := e.Approval
		m.pendingApproval = &a

	case event.TurnDone:
		// The turn settled — freeze anything still streaming, autosave, surface a
		// real error, and gate a plan-mode proposal on the user's approval.
		m.commitReasoning()
		m.commitPending()
		m.state = tuiIdle
		_ = m.ctrl.Snapshot() // best-effort; never the user's problem mid-chat
		if e.Err != nil && e.Err.Error() != "" && !strings.Contains(e.Err.Error(), "context canceled") {
			m.commitLine(wrapForViewport(i18n.M.ErrorPrefix+" "+e.Err.Error(), m.width, lipgloss.Color("3")))
		}
		if e.Err == nil && m.planMode && strings.TrimSpace(m.turnAccumulator.String()) != "" {
			m.pendingPlan = "pending"
		}
	}
}

// finalizeStreamed freezes any in-progress reasoning + answer into scrollback so
// a following event line lands after them, preserving chronological order.
func (m *chatTUI) finalizeStreamed() {
	m.commitReasoning()
	m.commitPending()
}

func waitForAgentEvent(ch chan event.Event) tea.Cmd {
	return func() tea.Msg { return agentEventMsg(<-ch) }
}

func elapsedTick() tea.Cmd {
	return tea.Tick(time.Second, func(_ time.Time) tea.Msg { return elapsedTickMsg{} })
}

// runSlashCommand handles "/<cmd> <args>" input. Local commands queue their
// output to scrollback; MCP prompt / custom commands resolve to a model turn.
func (m *chatTUI) runSlashCommand(input string) tea.Cmd {
	cmd := strings.TrimSpace(strings.SplitN(input, " ", 2)[0])

	if strings.HasPrefix(cmd, "/mcp__") {
		return m.runMCPPrompt(input)
	}

	switch cmd {
	case "/compact":
		if err := m.ctrl.Compact(context.Background()); err != nil {
			m.notice(fmt.Sprintf("%s: %v", i18n.M.SlashCompactFailed, err))
			return nil
		}
		m.notice(i18n.M.SlashCompactDone)
		_ = m.ctrl.Snapshot()
	case "/new":
		if err := m.ctrl.NewSession(); err != nil {
			m.notice(fmt.Sprintf("%s: %v", i18n.M.SlashNewFailed, err))
			return nil
		}
		// Native scrollback keeps the old transcript; mark the fork with a fresh
		// banner and reset live state.
		m.pending.Reset()
		m.reasoning.Reset()
		m.turnAccumulator.Reset()
		m.pendingPlan = ""
		m.commitLine("")
		m.commitLine(strings.TrimRight(renderTUIBanner(m.label, "", m.width), "\n"))
		m.notice(i18n.M.SlashNewDone)
	case "/mcp":
		m.showMCPStatus()
	case "/help":
		m.notice(i18n.M.SlashHelp)
		if names := m.commandNames(); names != "" {
			m.notice("custom: " + names)
		}
	default:
		if c, ok := m.lookupCommand(strings.TrimPrefix(cmd, "/")); ok {
			args := strings.Fields(input)
			return m.startTurn(m.composeSent(c.Render(args[1:])), input)
		}
		m.notice(fmt.Sprintf("%s: %s", i18n.M.SlashUnknown, cmd))
	}
	return nil
}

// lookupCommand finds a custom slash command by name.
func (m *chatTUI) lookupCommand(name string) (command.Command, bool) {
	for _, c := range m.commands {
		if c.Name == name {
			return c, true
		}
	}
	return command.Command{}, false
}

// commandNames renders the custom command list for /help, "" when there are none.
func (m *chatTUI) commandNames() string {
	if len(m.commands) == 0 {
		return ""
	}
	names := make([]string, len(m.commands))
	for i, c := range m.commands {
		names[i] = "/" + c.Name
	}
	return strings.Join(names, " · ")
}

// showMCPStatus queues the connected MCP servers, their counts, and the prompt
// commands / resource refs they expose — the discovery surface for /mcp.
func (m *chatTUI) showMCPStatus() {
	if m.host == nil || len(m.host.Servers()) == 0 {
		m.notice(i18n.M.SlashMCPNone)
		return
	}
	servers := m.host.Servers()
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", dim(fmt.Sprintf("  · MCP servers (%d)", len(servers))))
	for _, s := range servers {
		fmt.Fprintf(&b, "    %s %s %s\n", accent("✓"), bold(s.Name),
			dim(fmt.Sprintf("(%s) — %d tools · %d prompts · %d resources", s.Transport, s.Tools, s.Prompts, s.Resources)))
	}
	for _, p := range m.host.Prompts() {
		fmt.Fprintf(&b, "      %s  %s\n", "/"+p.Name, dim(p.Description))
	}
	for _, r := range m.host.Resources() {
		label := r.Name
		if label == "" {
			label = r.Description
		}
		fmt.Fprintf(&b, "      %s  %s\n", "@"+r.Server+":"+r.URI, dim(label))
	}
	m.commitLine(strings.TrimRight(b.String(), "\n"))
}

// notice queues a dim informational line to scrollback.
func (m *chatTUI) notice(note string) {
	m.commitLine(dim("  · " + note))
}

// composeSent applies the plan-mode marker (when active) to a turn's input.
func (m *chatTUI) composeSent(text string) string {
	if m.planMode {
		return planModeMarker + "\n\n" + text
	}
	return text
}

// runMCPPrompt resolves a /mcp__server__prompt command: it maps positional args
// onto the prompt's declared arguments and returns a tea.Cmd that fetches the
// rendered prompt off the event loop, delivering a promptResolvedMsg.
func (m *chatTUI) runMCPPrompt(input string) tea.Cmd {
	fields := strings.Fields(input)
	name := strings.TrimPrefix(fields[0], "/")

	prompts := m.prompts()
	var found *plugin.Prompt
	for i := range prompts {
		if prompts[i].Name == name {
			found = &prompts[i]
			break
		}
	}
	if found == nil {
		m.notice(fmt.Sprintf("%s: /%s", i18n.M.SlashUnknown, name))
		return nil
	}

	args := map[string]string{}
	for i, a := range found.Args {
		if i+1 < len(fields) {
			args[a.Name] = fields[i+1]
		}
	}

	prompt := *found
	display := input
	return func() tea.Msg {
		text, err := prompt.Get(context.Background(), args)
		return promptResolvedMsg{display: display, sent: text, err: err}
	}
}

// replaySectionsFor turns a loaded session into scrollback blocks: user bubbles
// and assistant markdown. Tool messages are dropped — needed in session state
// but noise in the visible transcript on resume.
func replaySectionsFor(history []provider.Message, width int, renderer *mdRenderer) []string {
	var out []string
	for _, m := range history {
		switch m.Role {
		case provider.RoleUser:
			content := strings.TrimPrefix(m.Content, planModeMarker+"\n\n")
			out = append(out, renderUserBubble(content, width, false)+"\n\n")
		case provider.RoleAssistant:
			body := strings.TrimSpace(m.Content)
			if body == "" {
				continue
			}
			rendered := renderer.Render(body)
			if rendered == "" {
				rendered = body
			}
			out = append(out, rendered+"\n")
		}
	}
	return out
}

// renderTUIBanner is the title + tip + optional missing-key warning printed once
// at the top of the session.
func renderTUIBanner(label, missing string, width int) string {
	var b strings.Builder
	b.WriteString(accent("◆") + " " + bold("reasonix chat") + "  " + dim("· "+label) + "\n")
	b.WriteString(dim("  "+i18n.M.ChatTip) + "\n")
	if missing != "" {
		b.WriteString(wrapForViewport("  ! "+missing, width, lipgloss.Color("3")) + "\n")
	}
	return b.String()
}

// wrapForViewport hard-wraps text to fit width columns and colours every line.
func wrapForViewport(text string, width int, fg color.Color) string {
	if width <= 0 {
		width = 80
	}
	return lipgloss.NewStyle().
		Foreground(fg).
		Width(width).
		Render(text)
}

// renderUserBubble styles the just-submitted line with a filled dim background.
func renderUserBubble(line string, width int, planMode bool) string {
	prefix := "› "
	if planMode {
		prefix = "› [plan] "
	}
	if !colorEnabled {
		return "│ " + prefix + line
	}
	w := width - 4
	if w < 10 {
		w = 10
	}
	bubble := lipgloss.NewStyle().
		Background(lipgloss.Color("236")).
		Width(w).
		Padding(0, 1)
	return bubble.Render(prefix + line)
}

// eventSink is the event.Sink the agent emits to in TUI mode. Each event
// becomes an agentEventMsg. The channel is generously buffered so streaming
// bursts don't back-pressure the agent goroutine.
type eventSink struct {
	ch chan<- event.Event
}

func (s *eventSink) Emit(e event.Event) { s.ch <- e }

// compactArgs trims and caps a tool's raw JSON arguments for the dispatch line,
// matching the agent's headless rendering so the chat timeline reads the same.
func compactArgs(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > 120 {
		return string(r[:120]) + "..."
	}
	return s
}
