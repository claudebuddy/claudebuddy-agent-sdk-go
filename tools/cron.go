package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

// CronJob represents a scheduled recurring task.
type CronJob struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Schedule  string `json:"schedule"`
	Command   string `json:"command"`
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"createdAt"`
}

// CronStore manages cron jobs in memory.
type CronStore struct {
	mu      sync.Mutex
	jobs    map[string]*CronJob
	counter int64
}

// NewCronStore creates a new CronStore.
func NewCronStore() *CronStore { return &CronStore{jobs: make(map[string]*CronJob)} }

func (s *CronStore) Create(name, schedule, command string) *CronJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counter++
	id := fmt.Sprintf("cron_%d", s.counter)
	job := &CronJob{
		ID: id, Name: name, Schedule: schedule, Command: command,
		Enabled: true, CreatedAt: time.Now().Format(time.RFC3339),
	}
	s.jobs[id] = job
	return job
}

func (s *CronStore) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.jobs[id]; !ok {
		return false
	}
	delete(s.jobs, id)
	return true
}

func (s *CronStore) List() []CronJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]CronJob, 0, len(s.jobs))
	for _, j := range s.jobs {
		result = append(result, *j)
	}
	return result
}

// CronCreateTool creates a scheduled cron job.
type CronCreateTool struct{ Store *CronStore }

func NewCronCreateTool(store *CronStore) *CronCreateTool { return &CronCreateTool{Store: store} }
func (t *CronCreateTool) Name() string                   { return "CronCreate" }
func (t *CronCreateTool) Description() string {
	return "Create a scheduled recurring task (cron job)."
}
func (t *CronCreateTool) InputSchema() types.ToolInputSchema {
	return types.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"name":     map[string]interface{}{"type": "string"},
			"schedule": map[string]interface{}{"type": "string", "description": "Cron expression (e.g. \"*/5 * * * *\")"},
			"command":  map[string]interface{}{"type": "string"},
		},
		Required: []string{"name", "schedule", "command"},
	}
}
func (t *CronCreateTool) IsConcurrencySafe(input map[string]interface{}) bool { return false }
func (t *CronCreateTool) IsReadOnly(input map[string]interface{}) bool        { return false }
func (t *CronCreateTool) Call(ctx context.Context, input map[string]interface{}, tCtx *types.ToolUseContext) (*types.ToolResult, error) {
	name, _ := input["name"].(string)
	schedule, _ := input["schedule"].(string)
	command, _ := input["command"].(string)
	job := t.Store.Create(name, schedule, command)
	return textResult(fmt.Sprintf("Cron job created: %s \"%s\" schedule=\"%s\"", job.ID, job.Name, job.Schedule)), nil
}

// CronDeleteTool deletes a cron job.
type CronDeleteTool struct{ Store *CronStore }

func NewCronDeleteTool(store *CronStore) *CronDeleteTool { return &CronDeleteTool{Store: store} }
func (t *CronDeleteTool) Name() string                   { return "CronDelete" }
func (t *CronDeleteTool) Description() string            { return "Delete a scheduled cron job." }
func (t *CronDeleteTool) InputSchema() types.ToolInputSchema {
	return types.ToolInputSchema{
		Type:       "object",
		Properties: map[string]interface{}{"id": map[string]interface{}{"type": "string"}},
		Required:   []string{"id"},
	}
}
func (t *CronDeleteTool) IsConcurrencySafe(input map[string]interface{}) bool { return false }
func (t *CronDeleteTool) IsReadOnly(input map[string]interface{}) bool        { return false }
func (t *CronDeleteTool) Call(ctx context.Context, input map[string]interface{}, tCtx *types.ToolUseContext) (*types.ToolResult, error) {
	id, _ := input["id"].(string)
	if !t.Store.Delete(id) {
		return errorResult(fmt.Sprintf("Cron job not found: %s", id)), nil
	}
	return textResult(fmt.Sprintf("Cron job deleted: %s", id)), nil
}

// CronListTool lists all cron jobs.
type CronListTool struct{ Store *CronStore }

func NewCronListTool(store *CronStore) *CronListTool { return &CronListTool{Store: store} }
func (t *CronListTool) Name() string                 { return "CronList" }
func (t *CronListTool) Description() string          { return "List all scheduled cron jobs." }
func (t *CronListTool) InputSchema() types.ToolInputSchema {
	return types.ToolInputSchema{Type: "object", Properties: map[string]interface{}{}}
}
func (t *CronListTool) IsConcurrencySafe(input map[string]interface{}) bool { return true }
func (t *CronListTool) IsReadOnly(input map[string]interface{}) bool        { return true }
func (t *CronListTool) Call(ctx context.Context, input map[string]interface{}, tCtx *types.ToolUseContext) (*types.ToolResult, error) {
	jobs := t.Store.List()
	if len(jobs) == 0 {
		return textResult("No cron jobs scheduled."), nil
	}
	var lines []string
	for _, j := range jobs {
		enabled := "+"
		if !j.Enabled {
			enabled = "-"
		}
		cmd := j.Command
		if len(cmd) > 50 {
			cmd = cmd[:50]
		}
		lines = append(lines, fmt.Sprintf("[%s] %s \"%s\" schedule=\"%s\" command=\"%s\"", j.ID, enabled, j.Name, j.Schedule, cmd))
	}
	return textResult(strings.Join(lines, "\n")), nil
}

// RemoteTriggerTool manages remote scheduled agent triggers (stub implementation).
type RemoteTriggerTool struct{}

func NewRemoteTriggerTool() *RemoteTriggerTool { return &RemoteTriggerTool{} }
func (t *RemoteTriggerTool) Name() string      { return "RemoteTrigger" }
func (t *RemoteTriggerTool) Description() string {
	return "Manage remote scheduled agent triggers. Requires a connected remote backend."
}
func (t *RemoteTriggerTool) InputSchema() types.ToolInputSchema {
	return types.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"action":   map[string]interface{}{"type": "string", "enum": []string{"list", "get", "create", "update", "run"}},
			"id":       map[string]interface{}{"type": "string"},
			"name":     map[string]interface{}{"type": "string"},
			"schedule": map[string]interface{}{"type": "string"},
			"prompt":   map[string]interface{}{"type": "string"},
		},
		Required: []string{"action"},
	}
}
func (t *RemoteTriggerTool) IsConcurrencySafe(input map[string]interface{}) bool { return true }
func (t *RemoteTriggerTool) IsReadOnly(input map[string]interface{}) bool        { return false }
func (t *RemoteTriggerTool) Call(ctx context.Context, input map[string]interface{}, tCtx *types.ToolUseContext) (*types.ToolResult, error) {
	action, _ := input["action"].(string)
	return textResult(fmt.Sprintf(
		"RemoteTrigger %s: This feature requires a connected remote backend. Use CronCreate/CronList/CronDelete for local scheduling.",
		action,
	)), nil
}
