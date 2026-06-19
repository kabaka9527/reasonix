package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"reasonix/internal/provider"
)

// ---- list_sessions tool -----------------------------------------------------

// listSessionsTool lists all saved conversation sessions in a session directory.
type listSessionsTool struct {
	sessionDir string
}

// NewListSessionsTool creates a tool that lists saved sessions.
func NewListSessionsTool(sessionDir string) *listSessionsTool {
	return &listSessionsTool{sessionDir: sessionDir}
}

func (t *listSessionsTool) Name() string   { return "list_sessions" }
func (t *listSessionsTool) ReadOnly() bool { return true }

func (t *listSessionsTool) Description() string {
	return "List all saved AI conversation sessions. Returns timestamp, model, turn count, preview, and file for each session, newest first. Use read_session to view the full conversation."
}

func (t *listSessionsTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"required":[]}`)
}

func (t *listSessionsTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	type info struct {
		path    string
		modTime time.Time
		turns   int
	}
	var sessions []info

	entries, err := os.ReadDir(t.sessionDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "No sessions found.\n", nil
		}
		return "", fmt.Errorf("list_sessions: %w", err)
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		full := filepath.Join(t.sessionDir, e.Name())
		fi, err := e.Info()
		if err != nil {
			continue
		}
		// Skip sessions that have never had user interaction (0 turns).
		turns := countUserTurns(full)
		if turns == 0 {
			continue
		}
		sessions = append(sessions, info{path: full, modTime: fi.ModTime(), turns: turns})
	}

	if len(sessions) == 0 {
		return "No sessions found.\n", nil
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].modTime.After(sessions[j].modTime)
	})

	var b strings.Builder
	fmt.Fprintf(&b, "# Saved Sessions (%d total)\n\n", len(sessions))
	b.WriteString("| # | Timestamp | Model | Turns | File\n")
	b.WriteString("|---|-----------|-------|-------|-----\n")
	for i, s := range sessions {
		ts := s.modTime.Format("2006-01-02 15:04")
		model := modelFromPath(s.path)
		fmt.Fprintf(&b, "| %d | %s | %s | %d | `%s`\n",
			i+1, ts, model, s.turns, filepath.Base(s.path))
	}
	b.WriteString("\nUse `read_session` with the filename under \"File\" to view the full conversation.\n")
	return b.String(), nil
}

// ---- read_session tool ------------------------------------------------------

// readSessionTool reads a saved session and returns its conversation as text.
type readSessionTool struct {
	sessionDir string
}

// NewReadSessionTool creates a tool that reads saved sessions.
func NewReadSessionTool(sessionDir string) *readSessionTool {
	return &readSessionTool{sessionDir: sessionDir}
}

func (t *readSessionTool) Name() string   { return "read_session" }
func (t *readSessionTool) ReadOnly() bool { return true }

func (t *readSessionTool) Description() string {
	return "Read a saved AI conversation session by its file name (e.g. \"20260618-231556.000000000-gpt-4.jsonl\"). Returns the full conversation as readable text, organized by speaker turn. Shows system prompts, user messages, assistant responses (including reasoning), tool calls, and tool results."
}

func (t *readSessionTool) Schema() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "session": {
      "type": "string",
      "description": "Session file name (e.g. \"20260618-231556.000000000-gpt-4.jsonl\") or full path. Use list_sessions to see available sessions."
    },
    "max_turns": {
      "type": "integer",
      "description": "Maximum number of user+assistant turns to return (default 50, 0 = no limit). Use a smaller number (e.g. 20) for a quick overview."
    }
  },
  "required": ["session"]
}`)
}

func (t *readSessionTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var params struct {
		Session  string `json:"session"`
		MaxTurns *int   `json:"max_turns"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return "", fmt.Errorf("read_session: invalid args: %w", err)
	}
	if params.Session == "" {
		return "", fmt.Errorf("read_session: 'session' argument is required")
	}

	sessionPath := params.Session
	// If it's just a filename (no path separator), resolve relative to sessionDir
	if !strings.Contains(sessionPath, string(filepath.Separator)) && !strings.Contains(sessionPath, "/") {
		sessionPath = filepath.Join(t.sessionDir, sessionPath)
	}
	// Guard against path traversal
	sessionPath = filepath.Clean(sessionPath)
	dir := filepath.Clean(t.sessionDir)
	if !strings.HasPrefix(sessionPath, dir+string(filepath.Separator)) && sessionPath != dir {
		return "", fmt.Errorf("read_session: path %q is outside the session directory", params.Session)
	}

	ses, err := loadSessionJSONL(sessionPath)
	if err != nil {
		return "", fmt.Errorf("read_session: %w", err)
	}
	msgs := ses
	if len(msgs) == 0 {
		return "Session is empty.\n", nil
	}

	maxTurns := 50
	if params.MaxTurns != nil && *params.MaxTurns > 0 {
		maxTurns = *params.MaxTurns
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Session: %s\n", filepath.Base(sessionPath))
	fmt.Fprintf(&b, "Messages: %d\n\n", len(msgs))

	turnCount := 0
	for _, m := range msgs {
		switch m.Role {
		case provider.RoleSystem:
			b.WriteString("## System Prompt\n\n")
			b.WriteString(m.Content)
			b.WriteString("\n\n")

		case provider.RoleUser:
			turnCount++
			if maxTurns > 0 && turnCount > maxTurns {
				b.WriteString("... (truncated, use `max_turns` to increase limit)\n")
				break
			}
			fmt.Fprintf(&b, "## User (turn %d)\n\n", turnCount)
			b.WriteString(m.Content)
			b.WriteString("\n\n")

		case provider.RoleAssistant:
			fmt.Fprintf(&b, "## Assistant (turn %d)\n\n", max(turnCount, 1))
			if m.ReasoningContent != "" {
				const maxReasoning = 500
				rc := m.ReasoningContent
				if len(rc) > maxReasoning {
					rc = rc[:maxReasoning] + "...\n[truncated]"
				}
				b.WriteString("### Reasoning\n\n")
				b.WriteString(rc)
				b.WriteString("\n\n")
			}
			if m.Content != "" {
				b.WriteString(m.Content)
				b.WriteString("\n\n")
			}
			if len(m.ToolCalls) > 0 {
				b.WriteString("### Tool Calls\n\n")
				for _, tc := range m.ToolCalls {
					fmt.Fprintf(&b, "- `%s(%s)`\n", tc.Name, string(tc.Arguments))
				}
				b.WriteString("\n")
			}

		case provider.RoleTool:
			fmt.Fprintf(&b, "### Tool Result: %s\n\n", m.Name)
			const maxToolResult = 1000
			content := m.Content
			if len(content) > maxToolResult {
				content = content[:maxToolResult] + "...\n[truncated]"
			}
			b.WriteString(content)
			b.WriteString("\n\n")
		}
	}

	return b.String(), nil
}

// ---- helpers ----------------------------------------------------------------

// loadSessionJSONL reads a JSONL session file into a message slice.
func loadSessionJSONL(path string) ([]provider.Message, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	defer f.Close()
	var msgs []provider.Message
	dec := json.NewDecoder(f)
	for {
		var m provider.Message
		if err := dec.Decode(&m); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("decode: %w", err)
		}
		msgs = append(msgs, m)
	}
	return msgs, nil
}

// countUserTurns returns the number of user-role messages in a JSONL file.
// Errors (missing file, malformed JSON) are silently treated as 0 turns
// so the session is excluded from listings rather than breaking the list.
func countUserTurns(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	turns := 0
	for {
		var m provider.Message
		if err := dec.Decode(&m); err != nil {
			break
		}
		if m.Role == provider.RoleUser {
			turns++
		}
	}
	return turns
}

// modelFromPath extracts the model name from a session file path.
// Filename format: "20060102-150405.000000000-model-name.jsonl"
func modelFromPath(path string) string {
	name := filepath.Base(path)
	name = strings.TrimSuffix(name, ".jsonl")
	// Skip the first two segments (timestamp.microseconds-model...)
	// Format: TIMESTAMP.MICROSECONDS-MODEL
	firstDash := strings.Index(name, "-")
	if firstDash < 0 {
		return "(unknown)"
	}
	rest := name[firstDash+1:]
	secondDash := strings.Index(rest, "-")
	if secondDash < 0 {
		return rest
	}
	// Model is everything after the second dash
	return rest[secondDash+1:]
}
