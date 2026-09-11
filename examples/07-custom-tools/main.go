// Example 7: Custom Tools
//
// Shows how to define and use custom tools alongside built-in tools.
//
// Run: go run ./examples/07-custom-tools/
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/agent"
	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

// weatherTool is a custom tool that returns weather data.
type weatherTool struct{}

func (t *weatherTool) Name() string { return "GetWeather" }
func (t *weatherTool) Description() string {
	return "Get current weather for a city. Returns temperature and conditions."
}
func (t *weatherTool) InputSchema() types.ToolInputSchema {
	return types.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"city": map[string]interface{}{
				"type":        "string",
				"description": `City name (e.g., "Tokyo", "London")`,
			},
		},
		Required: []string{"city"},
	}
}
func (t *weatherTool) IsConcurrencySafe(input map[string]interface{}) bool { return true }
func (t *weatherTool) IsReadOnly(input map[string]interface{}) bool        { return true }

func (t *weatherTool) Call(ctx context.Context, input map[string]interface{}, tCtx *types.ToolUseContext) (*types.ToolResult, error) {
	city, _ := input["city"].(string)
	temps := map[string]int{
		"tokyo": 22, "london": 14, "beijing": 25, "new york": 18, "paris": 16,
	}
	temp, ok := temps[strings.ToLower(city)]
	if !ok {
		temp = 20
	}
	result := fmt.Sprintf("Weather in %s: %d°C, partly cloudy", city, temp)
	return &types.ToolResult{
		Content: []types.ContentBlock{{Type: types.ContentBlockText, Text: result}},
	}, nil
}

// calculatorTool is a custom tool that evaluates simple math expressions.
type calculatorTool struct{}

func (t *calculatorTool) Name() string { return "Calculator" }
func (t *calculatorTool) Description() string {
	return "Evaluate a simple mathematical expression with two numbers and an operator (+, -, *, /, **)."
}
func (t *calculatorTool) InputSchema() types.ToolInputSchema {
	return types.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"expression": map[string]interface{}{
				"type":        "string",
				"description": `Math expression (e.g., "2 ** 10", "42 * 17")`,
			},
		},
		Required: []string{"expression"},
	}
}
func (t *calculatorTool) IsConcurrencySafe(input map[string]interface{}) bool { return true }
func (t *calculatorTool) IsReadOnly(input map[string]interface{}) bool        { return true }

func (t *calculatorTool) Call(ctx context.Context, input map[string]interface{}, tCtx *types.ToolUseContext) (*types.ToolResult, error) {
	expr, _ := input["expression"].(string)
	result, err := evalSimpleExpr(expr)
	var text string
	if err != nil {
		text = fmt.Sprintf("Error: %v", err)
	} else {
		text = fmt.Sprintf("%s = %s", expr, result)
	}
	return &types.ToolResult{
		Content: []types.ContentBlock{{Type: types.ContentBlockText, Text: text}},
	}, nil
}

// evalSimpleExpr evaluates expressions like "2 ** 10", "42 * 17 + 3"
func evalSimpleExpr(expr string) (string, error) {
	// Simple two-operand evaluation
	for _, op := range []string{"**", "*", "/", "+", "-"} {
		if idx := strings.Index(expr, op); idx > 0 {
			left := strings.TrimSpace(expr[:idx])
			right := strings.TrimSpace(expr[idx+len(op):])
			a, err1 := strconv.ParseFloat(left, 64)
			b, err2 := strconv.ParseFloat(right, 64)
			if err1 != nil || err2 != nil {
				continue
			}
			var result float64
			switch op {
			case "**":
				result = math.Pow(a, b)
			case "*":
				result = a * b
			case "/":
				if b == 0 {
					return "", fmt.Errorf("division by zero")
				}
				result = a / b
			case "+":
				result = a + b
			case "-":
				result = a - b
			}
			if result == float64(int64(result)) {
				return fmt.Sprintf("%d", int64(result)), nil
			}
			return fmt.Sprintf("%.4f", result), nil
		}
	}
	return "", fmt.Errorf("cannot evaluate: %s", expr)
}

func main() {
	fmt.Println("--- Example 7: Custom Tools ---")

	model := os.Getenv("CLAUDEBUDDY_MODEL")
	if model == "" {
		model = "sonnet-4-6"
	}

	a := agent.New(agent.Options{
		Model:    model,
		MaxTurns: 10,
		CustomTools: []types.Tool{
			&weatherTool{},
			&calculatorTool{},
		},
	})
	defer a.Close()

	ctx := context.Background()
	a.Init(ctx)

	fmt.Printf("Loaded tools with 2 custom tools (GetWeather, Calculator)\n\n")

	events, errs := a.Query(ctx,
		"What is the weather in Tokyo and London? Also calculate 2 ** 10 * 3. Be brief.",
	)

	for event := range events {
		if event.Type == types.MessageTypeAssistant && event.Message != nil {
			for _, block := range event.Message.Content {
				switch block.Type {
				case types.ContentBlockToolUse:
					inputJSON, _ := json.Marshal(block.Input)
					fmt.Printf("[%s] %s\n", block.Name, string(inputJSON))
				case types.ContentBlockText:
					if strings.TrimSpace(block.Text) != "" {
						fmt.Printf("\n%s\n", block.Text)
					}
				}
			}
		}
		if event.Type == types.MessageTypeResult {
			fmt.Println("\n--- Done ---")
		}
	}

	if err := <-errs; err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
