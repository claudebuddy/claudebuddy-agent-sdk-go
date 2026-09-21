package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

// NotebookEditTool edits Jupyter notebook (.ipynb) cells.
type NotebookEditTool struct{}

// NewNotebookEditTool creates a new NotebookEditTool.
func NewNotebookEditTool() *NotebookEditTool { return &NotebookEditTool{} }

func (t *NotebookEditTool) Name() string { return "NotebookEdit" }
func (t *NotebookEditTool) Description() string {
	return "Edit Jupyter notebook (.ipynb) cells. Can insert, replace, or delete cells."
}
func (t *NotebookEditTool) InputSchema() types.ToolInputSchema {
	return types.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"file_path":   map[string]interface{}{"type": "string", "description": "Path to the .ipynb file"},
			"command":     map[string]interface{}{"type": "string", "enum": []string{"insert", "replace", "delete"}},
			"cell_number": map[string]interface{}{"type": "number", "description": "Cell index (0-based)"},
			"cell_type":   map[string]interface{}{"type": "string", "enum": []string{"code", "markdown"}},
			"source":      map[string]interface{}{"type": "string", "description": "Cell content (for insert/replace)"},
		},
		Required: []string{"file_path", "command", "cell_number"},
	}
}
func (t *NotebookEditTool) IsConcurrencySafe(input map[string]interface{}) bool { return false }
func (t *NotebookEditTool) IsReadOnly(input map[string]interface{}) bool        { return false }

func (t *NotebookEditTool) Call(ctx context.Context, input map[string]interface{}, tCtx *types.ToolUseContext) (*types.ToolResult, error) {
	filePath, _ := input["file_path"].(string)
	if !filepath.IsAbs(filePath) && tCtx != nil {
		filePath = filepath.Join(tCtx.WorkingDir, filePath)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return errorResult(fmt.Sprintf("Error reading notebook: %s", err)), nil
	}

	var notebook map[string]interface{}
	if err := json.Unmarshal(data, &notebook); err != nil {
		return errorResult("Invalid notebook format"), nil
	}

	cells, ok := notebook["cells"].([]interface{})
	if !ok {
		return errorResult("Invalid notebook: missing cells array"), nil
	}

	command, _ := input["command"].(string)
	cellNumber := toInt(input["cell_number"])
	if cellNumber < 0 {
		return errorResult(fmt.Sprintf("Invalid cell number: %d", cellNumber)), nil
	}
	source, _ := input["source"].(string)
	cellType, _ := input["cell_type"].(string)

	switch command {
	case "insert":
		ct := cellType
		if ct == "" {
			ct = "code"
		}
		newCell := map[string]interface{}{
			"cell_type": ct,
			"source":    splitNotebookSource(source),
			"metadata":  map[string]interface{}{},
		}
		if ct != "markdown" {
			newCell["outputs"] = []interface{}{}
			newCell["execution_count"] = nil
		}
		idx := cellNumber
		if idx > len(cells) {
			idx = len(cells)
		}
		newCells := make([]interface{}, 0, len(cells)+1)
		newCells = append(newCells, cells[:idx]...)
		newCells = append(newCells, newCell)
		newCells = append(newCells, cells[idx:]...)
		notebook["cells"] = newCells

	case "replace":
		if cellNumber >= len(cells) {
			return errorResult(fmt.Sprintf("Cell %d does not exist (notebook has %d cells)", cellNumber, len(cells))), nil
		}
		cell, ok := cells[cellNumber].(map[string]interface{})
		if !ok {
			return errorResult(fmt.Sprintf("Cell %d is not a valid cell object", cellNumber)), nil
		}
		cell["source"] = splitNotebookSource(source)
		if cellType != "" {
			cell["cell_type"] = cellType
		}

	case "delete":
		if cellNumber >= len(cells) {
			return errorResult(fmt.Sprintf("Cell %d does not exist (notebook has %d cells)", cellNumber, len(cells))), nil
		}
		notebook["cells"] = append(cells[:cellNumber], cells[cellNumber+1:]...)

	default:
		return errorResult(fmt.Sprintf("Unknown command: %s", command)), nil
	}

	out, err := json.MarshalIndent(notebook, "", " ")
	if err != nil {
		return errorResult(fmt.Sprintf("Error marshaling notebook: %s", err)), nil
	}
	if err := os.WriteFile(filePath, out, 0644); err != nil {
		return errorResult(fmt.Sprintf("Error writing notebook: %s", err)), nil
	}

	return textResult(fmt.Sprintf("Notebook %s: cell %d in %s", command, cellNumber, filePath)), nil
}

// splitNotebookSource splits source string into notebook line array format.
func splitNotebookSource(source string) []string {
	if source == "" {
		return []string{}
	}
	lines := strings.Split(source, "\n")
	result := make([]string, len(lines))
	for i, line := range lines {
		if i < len(lines)-1 {
			result[i] = line + "\n"
		} else {
			result[i] = line
		}
	}
	return result
}
