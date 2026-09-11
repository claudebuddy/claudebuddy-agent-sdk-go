package costtracker

import (
	"testing"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

func TestLedgerSnapshotsAreIndependentAndAggregateByModel(t *testing.T) {
	ledger := NewLedger()
	ledger.Add("model-a", types.Usage{InputTokens: 10, OutputTokens: 2}, 0.25)
	ledger.Add("model-a", types.Usage{InputTokens: 5, CacheReadInputTokens: 3}, 0.10)
	ledger.Add("model-b", types.Usage{OutputTokens: 7}, 0.50)

	first := ledger.Snapshot()
	if first.Cost != 0.85 || first.Usage.InputTokens != 15 || first.Usage.OutputTokens != 9 {
		t.Fatalf("snapshot = %+v", first)
	}
	if got := first.ModelUsage["model-a"]; got.InputTokens != 15 || got.OutputTokens != 2 || got.CacheReadInputTokens != 3 {
		t.Fatalf("model-a usage = %+v", got)
	}

	first.ModelUsage["model-a"] = types.Usage{InputTokens: 999}
	second := ledger.Snapshot()
	if second.ModelUsage["model-a"].InputTokens != 15 {
		t.Fatalf("snapshot map aliases ledger: %+v", second.ModelUsage)
	}
}
