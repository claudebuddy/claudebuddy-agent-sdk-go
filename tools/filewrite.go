package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/tools/diff"
	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

// FileWriteTool writes content to files with staleness detection and diff generation.
type FileWriteTool struct{}

func NewFileWriteTool() *FileWriteTool { return &FileWriteTool{} }

func (t *FileWriteTool) Name() string { return "Write" }

func (t *FileWriteTool) Description() string {
	return `Writes a file to the local filesystem.

Usage:
- This tool will overwrite the existing file if there is one at the provided path.
- If this is an existing file, you MUST use the Read tool first. This tool will fail otherwise.
- Prefer the Edit tool for modifying existing files — it only sends the diff.
- Only use this tool to create new files or for complete rewrites.
- NEVER create documentation files (*.md) or README files unless explicitly requested.`
}

func (t *FileWriteTool) InputSchema() types.ToolInputSchema {
	return types.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"file_path": map[string]interface{}{
				"type":        "string",
				"description": "The absolute path to the file to write (must be absolute, not relative)",
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "The content to write to the file",
			},
		},
		Required: []string{"file_path", "content"},
	}
}

func (t *FileWriteTool) IsConcurrencySafe(input map[string]interface{}) bool { return false }
func (t *FileWriteTool) IsReadOnly(input map[string]interface{}) bool        { return false }

func (t *FileWriteTool) Call(ctx context.Context, input map[string]interface{}, tCtx *types.ToolUseContext) (*types.ToolResult, error) {
	filePath, _ := input["file_path"].(string)
	content, _ := input["content"].(string)

	if filePath == "" {
		return errorResult("file_path is required"), nil
	}

	if !filepath.IsAbs(filePath) && tCtx != nil && tCtx.WorkingDir != "" {
		filePath = filepath.Join(tCtx.WorkingDir, filePath)
	}

	// Check if file exists
	existingData, readErr := os.ReadFile(filePath)
	isCreate := os.IsNotExist(readErr)

	// For existing files, check staleness
	if readErr == nil && tCtx != nil && tCtx.ReadFileState != nil {
		if state, ok := tCtx.ReadFileState[filePath]; ok {
			// Verify content hasn't changed since last read
			if state.Content != string(existingData) {
				info, _ := os.Stat(filePath)
				if info != nil {
					modTime := info.ModTime().UnixMilli()
					if modTime > state.Timestamp {
						return errorResult(fmt.Sprintf(
							"File %s has been modified since last read. Please re-read before writing.",
							filePath)), nil
					}
				}
			}
		}
	}

	// Ensure parent directory exists
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return errorResult(fmt.Sprintf("Failed to create directory %s: %v", dir, err)), nil
	}

	// Write file
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		return errorResult(fmt.Sprintf("Failed to write file: %v", err)), nil
	}

	// Update file state cache
	if tCtx != nil && tCtx.ReadFileState != nil {
		tCtx.ReadFileState[filePath] = &types.FileReadState{
			Content:   content,
			Timestamp: time.Now().UnixMilli(),
		}
	}

	writeType := "update"
	if isCreate {
		writeType = "create"
	}

	// Generate diff for updates
	var patch string
	if !isCreate && readErr == nil {
		patch = diff.UnifiedDiff(filePath, string(existingData), content)
	}

	return &types.ToolResult{
		Data: map[string]interface{}{
			"type":     writeType,
			"filePath": filePath,
			"patch":    patch,
		},
		Content: []types.ContentBlock{{
			Type: types.ContentBlockText,
			Text: fmt.Sprintf("Successfully wrote to %s (%s)", filePath, writeType),
		}},
	}, nil
}
