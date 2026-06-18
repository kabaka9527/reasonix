// Heartbeat task engine — scheduled AI prompts that create or update topics.
//
// Each task is a prompt submitted to a dedicated topic on a schedule.
// The config file (~/.reasonix/heartbeat-tasks.json) is human- and AI-editable;
// the engine runs the schedule in a background goroutine and exposes Wails
// bindings on App for the frontend panel.
//
// Design goal: minimal upstream intrusion — one file, zero changes to existing
// Go code (App field + startup line + bindings are the only touch points).

package main

import (
	"encoding/json"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ── Data model ──────────────────────────────────────────────────────────────

// HeartbeatTask defines a single scheduled prompt.
type HeartbeatTask struct {
	ID            string `json:"id"`
	Title         string `json:"title"`    // user-visible label
	Prompt        string `json:"prompt"`   // the prompt to submit
	Interval      string `json:"interval"` // e.g. "5m", "1h", "30s"
	Enabled       bool   `json:"enabled"`
	Scope         string `json:"scope,omitempty"`         // "global" or "project"
	WorkspaceRoot string `json:"workspaceRoot,omitempty"` // project root path when scope="project"
	TopicID       string `json:"topicId,omitempty"`       // created topic, reused on re-run
	LastRunAt     int64  `json:"lastRunAt,omitempty"`     // unix millis
	CreatedAt     int64  `json:"createdAt,omitempty"`
}

// heartbeatConfig is the on-disk format.
type heartbeatConfig struct {
	Tasks []HeartbeatTask `json:"tasks"`
}

// ── Engine ──────────────────────────────────────────────────────────────────

// HeartbeatEngine runs scheduled task execution in a background goroutine.
// It is owned by App and started during App.startup.
type HeartbeatEngine struct {
	mu      sync.Mutex
	tasks   []HeartbeatTask
	done    chan struct{}
	running bool
	app     *App // back-reference for CreateTopic & SubmitToTab
}

func newHeartbeatEngine(app *App) *HeartbeatEngine {
	return &HeartbeatEngine{
		app:  app,
		done: make(chan struct{}),
	}
}

// configPath returns the JSON file path.
func (e *HeartbeatEngine) configPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".reasonix", "heartbeat-tasks.json")
}

// loadTasks reads tasks from disk.
func (e *HeartbeatEngine) loadTasks() []HeartbeatTask {
	b, err := os.ReadFile(e.configPath())
	if err != nil {
		return nil
	}
	var cfg heartbeatConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		log.Printf("[heartbeat] invalid config: %v", err)
		return nil
	}
	return cfg.Tasks
}

// saveTasks writes tasks to disk atomically.
func (e *HeartbeatEngine) saveTasks(tasks []HeartbeatTask) error {
	if tasks == nil {
		tasks = []HeartbeatTask{}
	}
	cfg := heartbeatConfig{Tasks: tasks}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	path := e.configPath()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Start launches the scheduler goroutine.
func (e *HeartbeatEngine) Start() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running {
		return
	}
	e.tasks = e.loadTasks()
	e.running = true
	go e.loop()
	log.Printf("[heartbeat] engine started (%d tasks)", len(e.tasks))
}

// Stop signals the scheduler goroutine to exit.
func (e *HeartbeatEngine) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.running {
		return
	}
	e.running = false
	close(e.done)
}

// loop is the main scheduler loop — tick every 30s and check each enabled task.
func (e *HeartbeatEngine) loop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-e.done:
			return
		case <-ticker.C:
			e.tick()
		}
	}
}

// tick checks every enabled task and runs those whose interval has elapsed.
func (e *HeartbeatEngine) tick() {
	e.mu.Lock()
	tasks := append([]HeartbeatTask(nil), e.tasks...)
	e.mu.Unlock()

	now := time.Now()
	for i, t := range tasks {
		if !t.Enabled {
			continue
		}
		d, err := parseInterval(t.Interval)
		if err != nil || d <= 0 {
			continue
		}
		elapsed := now.Sub(time.UnixMilli(t.LastRunAt))
		if elapsed < d {
			continue
		}
		// Run this task
		tasks[i] = e.executeTask(t)
	}

	e.mu.Lock()
	e.tasks = tasks
	_ = e.saveTasks(tasks)
	e.mu.Unlock()
}

// executeTask runs one heartbeat: creates/opens topic, submits prompt.
func (e *HeartbeatEngine) executeTask(t HeartbeatTask) HeartbeatTask {
	title := "Heartbeat: " + t.Title
	scope := t.Scope
	workspaceRoot := t.WorkspaceRoot
	if scope == "" {
		scope = "global"
	}

	// If we already have a topicID, reuse it; otherwise create a new topic.
	var topicID = t.TopicID
	if topicID == "" {
		meta, err := e.app.CreateTopic(scope, workspaceRoot, title)
		if err != nil {
			log.Printf("[heartbeat] CreateTopic(%q): %v", t.Title, err)
			t.LastRunAt = time.Now().UnixMilli()
			return t
		}
		topicID = meta.ID
		t.TopicID = topicID
	}

	// Open the tab for the topic (creates one if needed)
	var tabMeta TabMeta
	var err error
	if scope == "project" && workspaceRoot != "" {
		tabMeta, err = e.app.OpenProjectTab(workspaceRoot, topicID)
	} else {
		tabMeta, err = e.app.OpenGlobalTab(topicID)
	}
	if err != nil {
		log.Printf("[heartbeat] OpenTab(%q): %v", t.Title, err)
		t.LastRunAt = time.Now().UnixMilli()
		return t
	}

	// Submit the prompt — the model responds asynchronously.
	// We use SubmitToTab so the output goes to the right tab
	// and the transcript shows the full prompt text.
	//
	// Wait for the tab's controller to be built (it's started
	// asynchronously in a goroutine by openTopicTab).
	for i := 0; i < 40; i++ {
		if ctrl := e.app.ctrlByTabID(tabMeta.ID); ctrl != nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	e.app.SubmitToTab(tabMeta.ID, t.Prompt)

	t.LastRunAt = time.Now().UnixMilli()
	if t.CreatedAt == 0 {
		t.CreatedAt = t.LastRunAt
	}
	return t
}

// ListTasks returns a copy of the current tasks (in-memory).
func (e *HeartbeatEngine) ListTasks() []HeartbeatTask {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]HeartbeatTask, len(e.tasks))
	copy(out, e.tasks)
	return out
}

// ReloadTasks reloads the task list from disk and replaces the in-memory copy.
func (e *HeartbeatEngine) ReloadTasks() []HeartbeatTask {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.tasks = e.loadTasks()
	out := make([]HeartbeatTask, len(e.tasks))
	copy(out, e.tasks)
	return out
}

// ReplaceTasks atomically replaces the task list and persists it.
func (e *HeartbeatEngine) ReplaceTasks(tasks []HeartbeatTask) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.tasks = tasks
	return e.saveTasks(tasks)
}

// TriggerNow runs a single task immediately by ID.
func (e *HeartbeatEngine) TriggerNow(id string) {
	e.mu.Lock()
	var target *HeartbeatTask
	for i := range e.tasks {
		if e.tasks[i].ID == id {
			target = &e.tasks[i]
			break
		}
	}
	e.mu.Unlock()
	if target == nil {
		return
	}
	e.mu.Lock()
	tasks := append([]HeartbeatTask(nil), e.tasks...)
	e.mu.Unlock()
	for i, t := range tasks {
		if t.ID == id {
			tasks[i] = e.executeTask(t)
			break
		}
	}
	e.mu.Lock()
	e.tasks = tasks
	_ = e.saveTasks(tasks)
	e.mu.Unlock()
}

// parseInterval converts a string like "5m", "1h", "30s" to time.Duration.
// Suffix after '|' is stripped (e.g. "24h|daily@09:00" → "24h").
func parseInterval(s string) (time.Duration, error) {
	if idx := strings.Index(s, "|"); idx >= 0 {
		s = s[:idx]
	}
	if len(s) == 0 {
		return 0, nil
	}
	// Support common suffixed intervals
	switch s[len(s)-1] {
	case 's', 'm', 'h':
		return time.ParseDuration(s)
	default:
		// Try "Xm" as default assumption
		return time.ParseDuration(s + "m")
	}
}

// ── Wails bindings on App ───────────────────────────────────────────────────

// HeartbeatListTasks returns all heartbeat tasks.
func (a *App) HeartbeatListTasks() []HeartbeatTask {
	if a.heartbeat == nil {
		return nil
	}
	return a.heartbeat.ListTasks()
}

// HeartbeatReloadTasks reloads tasks from disk and returns them.
func (a *App) HeartbeatReloadTasks() []HeartbeatTask {
	if a.heartbeat == nil {
		return nil
	}
	return a.heartbeat.ReloadTasks()
}

// HeartbeatSaveTasks replaces the full task list and persists it.
func (a *App) HeartbeatSaveTasks(tasks []HeartbeatTask) error {
	if a.heartbeat == nil {
		return nil
	}
	return a.heartbeat.ReplaceTasks(tasks)
}

// HeartbeatTriggerNow immediately executes the task with the given ID.
func (a *App) HeartbeatTriggerNow(id string) {
	if a.heartbeat == nil {
		return
	}
	a.heartbeat.TriggerNow(id)
}

// HeartbeatGenerateID returns a random id for new tasks.
func (a *App) HeartbeatGenerateID() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 12)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	return string(b)
}
