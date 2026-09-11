package tools

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

// Team represents a multi-agent team.
type Team struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Members   []string `json:"members"`
	CreatedAt string   `json:"createdAt"`
	Status    string   `json:"status"`
}

// TeamStore manages teams in memory.
type TeamStore struct {
	mu      sync.Mutex
	teams   map[string]*Team
	counter int64
}

// NewTeamStore creates a new TeamStore.
func NewTeamStore() *TeamStore { return &TeamStore{teams: make(map[string]*Team)} }

func (s *TeamStore) Create(name string, members []string) *Team {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counter++
	id := fmt.Sprintf("team_%d", s.counter)
	team := &Team{
		ID:        id,
		Name:      name,
		Members:   members,
		CreatedAt: time.Now().Format(time.RFC3339),
		Status:    "active",
	}
	s.teams[id] = team
	return team
}

func (s *TeamStore) Delete(id string) (*Team, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	team, ok := s.teams[id]
	if !ok {
		return nil, false
	}
	delete(s.teams, id)
	return team, true
}

func (s *TeamStore) List() []Team {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Team, 0, len(s.teams))
	for _, t := range s.teams {
		result = append(result, *t)
	}
	return result
}

// TeamCreateTool creates a multi-agent team.
type TeamCreateTool struct{ Store *TeamStore }

// NewTeamCreateTool creates a new TeamCreateTool.
func NewTeamCreateTool(store *TeamStore) *TeamCreateTool { return &TeamCreateTool{Store: store} }

func (t *TeamCreateTool) Name() string { return "TeamCreate" }
func (t *TeamCreateTool) Description() string {
	return "Create a multi-agent team for coordinated work."
}
func (t *TeamCreateTool) InputSchema() types.ToolInputSchema {
	return types.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"name":             map[string]interface{}{"type": "string"},
			"members":          map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
			"task_description": map[string]interface{}{"type": "string"},
		},
		Required: []string{"name"},
	}
}
func (t *TeamCreateTool) IsConcurrencySafe(input map[string]interface{}) bool { return false }
func (t *TeamCreateTool) IsReadOnly(input map[string]interface{}) bool        { return false }
func (t *TeamCreateTool) Call(ctx context.Context, input map[string]interface{}, tCtx *types.ToolUseContext) (*types.ToolResult, error) {
	name, _ := input["name"].(string)
	var members []string
	if raw, ok := input["members"].([]interface{}); ok {
		for _, m := range raw {
			if s, ok := m.(string); ok {
				members = append(members, s)
			}
		}
	}
	team := t.Store.Create(name, members)
	return textResult(fmt.Sprintf("Team created: %s \"%s\" with %d members", team.ID, team.Name, len(team.Members))), nil
}

// TeamDeleteTool disbands a team.
type TeamDeleteTool struct{ Store *TeamStore }

// NewTeamDeleteTool creates a new TeamDeleteTool.
func NewTeamDeleteTool(store *TeamStore) *TeamDeleteTool { return &TeamDeleteTool{Store: store} }

func (t *TeamDeleteTool) Name() string        { return "TeamDelete" }
func (t *TeamDeleteTool) Description() string { return "Disband a team and clean up resources." }
func (t *TeamDeleteTool) InputSchema() types.ToolInputSchema {
	return types.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"id": map[string]interface{}{"type": "string", "description": "Team ID to disband"},
		},
		Required: []string{"id"},
	}
}
func (t *TeamDeleteTool) IsConcurrencySafe(input map[string]interface{}) bool { return false }
func (t *TeamDeleteTool) IsReadOnly(input map[string]interface{}) bool        { return false }
func (t *TeamDeleteTool) Call(ctx context.Context, input map[string]interface{}, tCtx *types.ToolUseContext) (*types.ToolResult, error) {
	id, _ := input["id"].(string)
	team, ok := t.Store.Delete(id)
	if !ok {
		return errorResult(fmt.Sprintf("Team not found: %s", id)), nil
	}
	return textResult(fmt.Sprintf("Team disbanded: %s", team.Name)), nil
}
