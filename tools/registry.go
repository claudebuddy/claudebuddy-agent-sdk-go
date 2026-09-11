package tools

import (
	"sync"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

// Registry manages available tools.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]types.Tool
}

// NewRegistry creates an empty tool registry.
func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]types.Tool),
	}
}

// Register adds a tool to the registry.
func (r *Registry) Register(tool types.Tool) {
	r.RegisterNamed(tool.Name(), tool)
}

// RegisterNamed installs a reference with an already resolved name. It lets
// runtime owners publish tools under their map locks without invoking metadata.
func (r *Registry) RegisterNamed(name string, tool types.Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[name] = tool
}

// Get returns a tool by name, or nil if not found.
func (r *Registry) Get(name string) types.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.tools[name]
}

// All returns all registered tools.
func (r *Registry) All() []types.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]types.Tool, 0, len(r.tools))
	for _, t := range r.tools {
		result = append(result, t)
	}
	return result
}

// Names returns all registered tool names.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	return names
}

// Filter returns tools matching a filter function.
func (r *Registry) Filter(fn func(types.Tool) bool) []types.Tool {
	var result []types.Tool
	for _, t := range r.All() {
		if fn(t) {
			result = append(result, t)
		}
	}
	return result
}

// DefaultRegistry returns a registry with all built-in tools.
func DefaultRegistry() *Registry {
	r := NewRegistry()
	for _, t := range GetAllBaseTools() {
		r.Register(t)
	}
	return r
}

// GetAllBaseTools returns all built-in tool implementations.
func GetAllBaseTools() []types.Tool {
	store := NewTaskStore()
	todoStore := NewTodoStore()
	configStore := NewConfigStore()
	mailbox := NewMailbox()
	teamStore := NewTeamStore()
	planState := NewPlanModeState()
	worktreeStore := NewWorktreeStore()
	cronStore := NewCronStore()

	return []types.Tool{
		NewBashTool(),
		NewFileReadTool(),
		NewFileWriteTool(),
		NewFileEditTool(),
		NewGlobTool(),
		NewGrepTool(),
		NewWebFetchTool(),
		NewWebSearchTool(),
		&TaskCreateTool{Store: store},
		&TaskGetTool{Store: store},
		&TaskListTool{Store: store},
		&TaskUpdateTool{Store: store},
		&TaskStopTool{Store: store},
		&TaskOutputTool{Store: store},
		NewTodoWriteTool(todoStore),
		NewConfigTool(configStore),
		NewSendMessageTool(mailbox, "agent"),
		NewTeamCreateTool(teamStore),
		NewTeamDeleteTool(teamStore),
		NewEnterPlanModeTool(planState),
		NewExitPlanModeTool(planState),
		NewEnterWorktreeTool(worktreeStore),
		NewExitWorktreeTool(worktreeStore),
		NewListMcpResourcesTool(nil),
		NewReadMcpResourceTool(nil),
		NewCronCreateTool(cronStore),
		NewCronDeleteTool(cronStore),
		NewCronListTool(cronStore),
		NewRemoteTriggerTool(),
		NewNotebookEditTool(),
		NewLSPTool(),
	}
}
