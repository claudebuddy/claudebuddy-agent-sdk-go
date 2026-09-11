package costtracker

import (
	"sync"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

// LedgerSnapshot is an immutable-by-convention copy of Run accounting.
type LedgerSnapshot struct {
	Cost       float64                `json:"cost"`
	Usage      types.Usage            `json:"usage"`
	ModelUsage map[string]types.Usage `json:"model_usage"`
}

// Ledger is the concurrency-safe budget and usage owner for one Run and its
// descendants. Session aggregates are intentionally maintained separately.
type Ledger struct {
	mu         sync.RWMutex
	cost       float64
	usage      types.Usage
	modelUsage map[string]types.Usage
}

func NewLedger() *Ledger {
	return &Ledger{modelUsage: make(map[string]types.Usage)}
}

// Add records one completed model admission.
func (l *Ledger) Add(model string, usage types.Usage, cost float64) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cost += cost
	addUsage(&l.usage, usage)
	modelUsage := l.modelUsage[model]
	addUsage(&modelUsage, usage)
	l.modelUsage[model] = modelUsage
}

// Snapshot returns independently owned model accounting.
func (l *Ledger) Snapshot() LedgerSnapshot {
	if l == nil {
		return LedgerSnapshot{ModelUsage: map[string]types.Usage{}}
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	modelUsage := make(map[string]types.Usage, len(l.modelUsage))
	for model, usage := range l.modelUsage {
		modelUsage[model] = usage
	}
	return LedgerSnapshot{Cost: l.cost, Usage: l.usage, ModelUsage: modelUsage}
}

func addUsage(total *types.Usage, usage types.Usage) {
	total.InputTokens += usage.InputTokens
	total.OutputTokens += usage.OutputTokens
	total.CacheReadInputTokens += usage.CacheReadInputTokens
	total.CacheCreationInputTokens += usage.CacheCreationInputTokens
}

// EstimateCost calculates the SDK's approximate price for one usage record.
func EstimateCost(model string, usage *types.Usage) float64 {
	if usage == nil {
		return 0
	}
	return calculateCost(model, usage)
}
