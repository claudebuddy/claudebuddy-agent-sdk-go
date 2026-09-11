// Example 11: Web Chat UI
//
// A web-based chat interface for interacting with the agent.
// Shows streaming text, tool calls with input/output, and cost tracking.
//
// Run: go run ./examples/web/
// Then open http://localhost:8082
package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/agent"
	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

//go:embed index.html
var staticFS embed.FS

// SSE event types sent to the frontend.
type sseEvent struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

type textEvent struct {
	Text string `json:"text"`
}

type toolUseEvent struct {
	ID    string                 `json:"id"`
	Name  string                 `json:"name"`
	Input map[string]interface{} `json:"input"`
}

type toolResultEvent struct {
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
	IsError   bool   `json:"is_error"`
}

type resultEvent struct {
	NumTurns     int     `json:"num_turns"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	Cost         float64 `json:"cost"`
	DurationMs   int64   `json:"duration_ms"`
}

type thinkingEvent struct {
	Text string `json:"thinking"`
}

var (
	agentInstance *agent.Agent
	agentMu       sync.Mutex
)

func getOrCreateAgent() *agent.Agent {
	agentMu.Lock()
	defer agentMu.Unlock()

	if agentInstance == nil {
		model := os.Getenv("CLAUDEBUDDY_MODEL")
		if model == "" {
			model = "sonnet-4-6"
		}
		agentInstance = agent.New(agent.Options{
			Model:    model,
			MaxTurns: 20,
		})
		agentInstance.Init(context.Background())
	}
	return agentInstance
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8082"
	}

	// Serve static files
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		data, _ := staticFS.ReadFile("index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	})

	// Chat endpoint (SSE streaming)
	http.HandleFunc("/api/chat", handleChat)

	// New session
	http.HandleFunc("/api/new", handleNew)

	fmt.Printf("🚀 Web Chat running at http://localhost:%s\n", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func handleNew(w http.ResponseWriter, r *http.Request) {
	agentMu.Lock()
	if agentInstance != nil {
		agentInstance.Close()
	}
	agentInstance = nil
	agentMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Message == "" {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	// SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	a := getOrCreateAgent()
	ctx := r.Context()
	start := time.Now()

	events, errs := a.Query(ctx, req.Message)

	sendSSE := func(eventType string, data interface{}) {
		payload, _ := json.Marshal(sseEvent{Type: eventType, Data: data})
		fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
	}

	for event := range events {
		switch event.Type {
		case types.MessageTypeAssistant:
			if event.Message == nil {
				continue
			}
			for _, block := range event.Message.Content {
				switch block.Type {
				case types.ContentBlockText:
					if block.Text != "" {
						sendSSE("text", textEvent{Text: block.Text})
					}
				case types.ContentBlockToolUse:
					sendSSE("tool_use", toolUseEvent{
						ID:    block.ID,
						Name:  block.Name,
						Input: block.Input,
					})
				case types.ContentBlockThinking:
					if block.Thinking != "" {
						sendSSE("thinking", thinkingEvent{Text: block.Thinking})
					}
				}
			}

		case "tool_result":
			// Tool results emitted by the agent loop after tool execution
			if event.Message != nil {
				for _, block := range event.Message.Content {
					if block.Type == types.ContentBlockToolResult {
						content := ""
						for _, cb := range block.Content {
							if cb.Type == types.ContentBlockText {
								content += cb.Text
							}
						}
						sendSSE("tool_result", toolResultEvent{
							ToolUseID: block.ToolUseID,
							Content:   content,
							IsError:   block.IsError,
						})
					}
				}
			}

		case types.MessageTypeResult:
			inputTokens, outputTokens := 0, 0
			if event.Usage != nil {
				inputTokens = event.Usage.InputTokens
				outputTokens = event.Usage.OutputTokens
			}
			sendSSE("result", resultEvent{
				NumTurns:     event.NumTurns,
				InputTokens:  inputTokens,
				OutputTokens: outputTokens,
				Cost:         event.Cost,
				DurationMs:   time.Since(start).Milliseconds(),
			})
		}
	}

	select {
	case err := <-errs:
		if err != nil {
			sendSSE("error", map[string]string{"message": err.Error()})
		}
	default:
	}

	sendSSE("done", nil)
}
