package main

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"reasonix/internal/bot"
	"reasonix/internal/event"
)

const botForwardSendTimeout = 30 * time.Second

// ── Forward target ──────────────────────────────────────────────────────────

// botForwardTarget identifies one remote chat to send forwarded events to.
type botForwardTarget struct {
	ConnID   string
	Domain   string
	ChatID   string
	ChatType bot.ChatType
}

// ── Event forwarder ─────────────────────────────────────────────────────────

// botEventForwarder implements event.Sink and forwards relevant events to
// connected bot channels through the desktopBotRuntime. It is attached to a
// tabEventSink when a heartbeat task should push AI output to IM channels.
//
// It accumulates Text events and sends them as complete messages on TurnDone
// (and occasionally during generation when the buffer grows large enough), so
// the remote side sees progressive streaming output rather than one big blob.
type botEventForwarder struct {
	runtime *desktopBotRuntime
	targets []botForwardTarget

	mu  sync.Mutex
	buf strings.Builder
}

// newBotEventForwarder creates a forwarder that sends to all given targets.
// runtime may be nil — Emit calls are then no-ops.
func newBotEventForwarder(runtime *desktopBotRuntime, targets []botForwardTarget) *botEventForwarder {
	return &botEventForwarder{
		runtime: runtime,
		targets: targets,
	}
}

// Emit implements event.Sink. It forwards text and lifecycle events to the
// connected bot channels; reasoning, tool dispatch, and other internal events
// are dropped to avoid noisy IM output.
func (f *botEventForwarder) Emit(e event.Event) {
	if f.runtime == nil || len(f.targets) == 0 {
		return
	}
	switch e.Kind {
	case event.TurnStarted:
		f.mu.Lock()
		f.buf.Reset()
		f.mu.Unlock()

	case event.Text:
		f.mu.Lock()
		f.buf.WriteString(e.Text)
		size := f.buf.Len()
		f.mu.Unlock()
		// Flush opportunistically when the buffer crosses a threshold, so long
		// streams (e.g. "tell me three jokes") produce multiple messages.
		if size >= 400 {
			f.flush()
		}

	case event.TurnDone:
		f.flush()

	case event.ApprovalRequest:
		// Forward approval requests so the remote user can approve inline.
		text := "⚠️ 需要批准操作: " + e.Approval.Tool + " — " + e.Approval.Subject
		text += "\nID: " + e.Approval.ID
		f.sendToAll(text)

	case event.AskRequest:
		var qb strings.Builder
		qb.WriteString("❓ 需要回答问题:\n")
		for i, q := range e.Ask.Questions {
			if i > 0 {
				qb.WriteString("\n")
			}
			qb.WriteString(q.Prompt)
		}
		qb.WriteString("\nID: " + e.Ask.ID)
		f.sendToAll(qb.String())

	case event.Notice:
		if e.Level == event.LevelWarn {
			f.sendToAll("⚠️ " + e.Text)
		}

	case event.CompactionStarted:
		f.sendToAll("🔄 正在压缩上下文...")
	}
}

// flush sends the accumulated buffer as one message per target channel.
func (f *botEventForwarder) flush() {
	f.mu.Lock()
	text := strings.TrimSpace(f.buf.String())
	if text == "" {
		f.mu.Unlock()
		return
	}
	f.buf.Reset()
	f.mu.Unlock()

	f.sendToAll(text)
}

// sendToAll dispatches text to every target channel. Errors are logged and
// non-fatal; a failed target does not block other targets.
func (f *botEventForwarder) sendToAll(text string) {
	if f.runtime == nil || len(f.targets) == 0 || strings.TrimSpace(text) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), botForwardSendTimeout)
	defer cancel()
	for _, tgt := range f.targets {
		_, err := f.runtime.SendToAdapter(ctx, tgt.ConnID, tgt.Domain, bot.OutboundMessage{
			ChatID:   tgt.ChatID,
			ChatType: tgt.ChatType,
			Text:     text,
		})
		if err != nil {
			log.Printf("[bot-forward] send to %s/%s failed: %v", tgt.ConnID, tgt.ChatID, err)
		}
	}
}
