package contextusage

import (
	"sync"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

// ContextUsage summarises how much of the context window is consumed.
type ContextUsage struct {
	TotalTokens      int                `json:"totalTokens"`
	MaxTokens        int                `json:"maxTokens"`
	Percentage       float64            `json:"percentage"`
	Model            string             `json:"model"`
	Categories       map[string]int     `json:"categories"`
	MessageBreakdown []MessageTokenInfo `json:"messageBreakdown,omitempty"`
}

// MessageTokenInfo describes token usage for a single message.
type MessageTokenInfo struct {
	Role   string `json:"role"`
	Tokens int    `json:"tokens"`
	UUID   string `json:"uuid,omitempty"`
}

// Default model context window sizes (tokens).
var defaultMaxTokens = map[string]int{
	"opus-4-6":   1_000_000,
	"sonnet-4-6": 200_000,
	"sonnet-4-5": 200_000,
	"haiku-4-5":  200_000,
}

// Rough per-character token estimate (1 token ~ 4 chars).
const charsPerToken = 4

// Tracker estimates and tracks context window usage.
type Tracker struct {
	mu        sync.RWMutex
	maxTokens int
	usage     *ContextUsage
}

// NewTracker creates a new context usage tracker.
func NewTracker() *Tracker {
	return &Tracker{
		maxTokens: 200_000, // sensible default
		usage: &ContextUsage{
			Categories: make(map[string]int),
		},
	}
}

// SetMaxTokens overrides the maximum context window size.
func (t *Tracker) SetMaxTokens(max int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.maxTokens = max
}

// Update re-estimates context usage from messages and tool count.
// If messages carry Usage info from the API, those values are used;
// otherwise a rough character-based estimate is applied.
func (t *Tracker) Update(model string, messages []types.Message, toolCount int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Resolve max tokens for the model.
	maxTok := t.maxTokens
	if m, ok := defaultMaxTokens[model]; ok && maxTok == 0 {
		maxTok = m
	}
	if maxTok == 0 {
		maxTok = 200_000
	}
	t.maxTokens = maxTok

	categories := make(map[string]int)
	var breakdown []MessageTokenInfo
	totalTokens := 0

	for _, msg := range messages {
		tokens := estimateMessageTokens(&msg)
		totalTokens += tokens
		breakdown = append(breakdown, MessageTokenInfo{
			Role:   msg.Role,
			Tokens: tokens,
			UUID:   msg.UUID,
		})

		// Categorise by content type.
		for _, block := range msg.Content {
			switch block.Type {
			case types.ContentBlockToolUse:
				categories["toolUse"] += tokens / max(len(msg.Content), 1)
			case types.ContentBlockToolResult:
				categories["toolResults"] += tokens / max(len(msg.Content), 1)
			}
		}
	}

	// Account for tool definitions in the system prompt.
	toolTokens := toolCount * 150 // rough estimate per tool schema
	categories["tools"] = toolTokens
	totalTokens += toolTokens

	pct := 0.0
	if maxTok > 0 {
		pct = float64(totalTokens) / float64(maxTok) * 100.0
	}

	t.usage = &ContextUsage{
		TotalTokens:      totalTokens,
		MaxTokens:        maxTok,
		Percentage:       pct,
		Model:            model,
		Categories:       categories,
		MessageBreakdown: breakdown,
	}
}

// GetUsage returns a copy of the current context usage.
func (t *Tracker) GetUsage() *ContextUsage {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.usage == nil {
		return nil
	}
	cp := *t.usage
	// Deep-copy maps and slices.
	cp.Categories = make(map[string]int, len(t.usage.Categories))
	for k, v := range t.usage.Categories {
		cp.Categories[k] = v
	}
	cp.MessageBreakdown = make([]MessageTokenInfo, len(t.usage.MessageBreakdown))
	copy(cp.MessageBreakdown, t.usage.MessageBreakdown)
	return &cp
}

// estimateMessageTokens returns a token count for a message.
// It prefers the API-reported usage when available.
func estimateMessageTokens(msg *types.Message) int {
	if msg.Usage != nil {
		total := msg.Usage.InputTokens + msg.Usage.OutputTokens
		if total > 0 {
			return total
		}
	}

	// Fallback: character-based estimate.
	chars := 0
	for _, block := range msg.Content {
		switch block.Type {
		case types.ContentBlockText:
			chars += len(block.Text)
		case types.ContentBlockToolUse:
			chars += len(block.Name) + estimateMapChars(block.Input)
		case types.ContentBlockToolResult:
			for _, sub := range block.Content {
				chars += len(sub.Text)
			}
		case types.ContentBlockThinking:
			chars += len(block.Thinking)
		}
	}

	tokens := chars / charsPerToken
	if tokens == 0 && chars > 0 {
		tokens = 1
	}
	// Add overhead for role, framing, etc.
	tokens += 4
	return tokens
}

// estimateMapChars gives a rough character count for a map.
func estimateMapChars(m map[string]interface{}) int {
	if len(m) == 0 {
		return 2 // "{}"
	}
	total := 2 // braces
	for k, v := range m {
		total += len(k) + 4 // key + quotes/colon/comma
		switch val := v.(type) {
		case string:
			total += len(val)
		default:
			total += 10 // rough estimate for numbers, bools, etc.
		}
	}
	return total
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
