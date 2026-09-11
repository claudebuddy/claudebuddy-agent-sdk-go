package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/tools/diff"
	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

// FileEditTool performs string replacements in files with diff generation,
// staleness detection, and line ending preservation.
type FileEditTool struct{}

func NewFileEditTool() *FileEditTool { return &FileEditTool{} }

func (t *FileEditTool) Name() string { return "Edit" }

func (t *FileEditTool) Description() string {
	return `Performs exact string replacements in files.

Usage:
- You must use the Read tool at least once before editing a file.
- The edit will FAIL if old_string is not unique in the file. Provide more context or use replace_all.
- Preserve exact indentation as it appears in the file.
- old_string and new_string must be different.`
}

func (t *FileEditTool) InputSchema() types.ToolInputSchema {
	return types.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"file_path": map[string]interface{}{
				"type":        "string",
				"description": "The absolute path to the file to modify",
			},
			"old_string": map[string]interface{}{
				"type":        "string",
				"description": "The text to find and replace",
			},
			"new_string": map[string]interface{}{
				"type":        "string",
				"description": "The replacement text (must be different from old_string)",
			},
			"replace_all": map[string]interface{}{
				"type":        "boolean",
				"description": "Replace all occurrences (default false)",
				"default":     false,
			},
		},
		Required: []string{"file_path", "old_string", "new_string"},
	}
}

func (t *FileEditTool) IsConcurrencySafe(input map[string]interface{}) bool { return false }
func (t *FileEditTool) IsReadOnly(input map[string]interface{}) bool        { return false }

func (t *FileEditTool) Call(ctx context.Context, input map[string]interface{}, tCtx *types.ToolUseContext) (*types.ToolResult, error) {
	filePath, _ := input["file_path"].(string)
	oldString, _ := input["old_string"].(string)
	newString, _ := input["new_string"].(string)
	replaceAll, _ := input["replace_all"].(bool)

	if filePath == "" {
		return errorResult("file_path is required"), nil
	}
	if oldString == "" {
		return errorResult("old_string is required"), nil
	}
	if oldString == newString {
		return errorResult("old_string and new_string must be different"), nil
	}

	if !filepath.IsAbs(filePath) && tCtx != nil && tCtx.WorkingDir != "" {
		filePath = filepath.Join(tCtx.WorkingDir, filePath)
	}

	// Read file
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return errorResult(fmt.Sprintf("File does not exist: %s", filePath)), nil
		}
		return errorResult(err.Error()), nil
	}

	// File size check
	if len(data) > maxFileSize {
		return errorResult(fmt.Sprintf("File is too large (%d bytes, max %d)", len(data), maxFileSize)), nil
	}

	content := string(data)
	originalContent := content

	// Detect line ending style
	lineEnding := detectLineEnding(content)

	// Staleness check
	if tCtx != nil && tCtx.ReadFileState != nil {
		if state, ok := tCtx.ReadFileState[filePath]; ok {
			if state.Content != content {
				info, _ := os.Stat(filePath)
				if info != nil {
					modTime := info.ModTime().UnixMilli()
					if modTime > state.Timestamp {
						return errorResult(fmt.Sprintf(
							"File %s has been modified since last read (read at %d, modified at %d). Please re-read the file before editing.",
							filePath, state.Timestamp, modTime)), nil
					}
				}
			}
		}
	}

	// Count occurrences
	count := strings.Count(content, oldString)
	if count == 0 {
		// Try with normalized quotes
		normalizedOld := normalizeQuotes(oldString)
		normalizedContent := normalizeQuotes(content)
		count = strings.Count(normalizedContent, normalizedOld)
		if count > 0 {
			return errorResult(fmt.Sprintf(
				"old_string not found with exact match, but found %d match(es) with normalized quotes. "+
					"Make sure you're using the same quote style as the file.", count)), nil
		}
		return errorResult(fmt.Sprintf(
			"old_string not found in %s. Make sure the string matches exactly, including whitespace and indentation.",
			filePath)), nil
	}

	if count > 1 && !replaceAll {
		return errorResult(fmt.Sprintf(
			"old_string found %d times in %s. Use replace_all=true to replace all occurrences, "+
				"or provide more context to make the match unique.",
			count, filePath)), nil
	}

	// Perform replacement
	var newContent string
	if replaceAll {
		newContent = strings.ReplaceAll(content, oldString, newString)
	} else {
		newContent = strings.Replace(content, oldString, newString, 1)
	}

	// Preserve line endings
	if lineEnding == "\r\n" && !strings.Contains(newString, "\r\n") {
		newContent = strings.ReplaceAll(newContent, "\n", "\r\n")
		// But don't double-convert \r\n
		newContent = strings.ReplaceAll(newContent, "\r\r\n", "\r\n")
	}

	// Write back
	if err := os.WriteFile(filePath, []byte(newContent), 0644); err != nil {
		return errorResult(fmt.Sprintf("Failed to write file: %v", err)), nil
	}

	// Update file state cache
	if tCtx != nil && tCtx.ReadFileState != nil {
		tCtx.ReadFileState[filePath] = &types.FileReadState{
			Content:   newContent,
			Timestamp: time.Now().UnixMilli(),
		}
	}

	replacements := 1
	if replaceAll {
		replacements = count
	}

	// Generate unified diff
	patch := diff.UnifiedDiff(filePath, originalContent, newContent)

	return &types.ToolResult{
		Data: map[string]interface{}{
			"filePath":     filePath,
			"replacements": replacements,
			"oldString":    oldString,
			"newString":    newString,
			"replaceAll":   replaceAll,
			"patch":        patch,
		},
		Content: []types.ContentBlock{{
			Type: types.ContentBlockText,
			Text: fmt.Sprintf("Successfully edited %s (%d replacement(s) made)", filePath, replacements),
		}},
	}, nil
}

// detectLineEnding detects the predominant line ending style.
func detectLineEnding(content string) string {
	crlfCount := strings.Count(content, "\r\n")
	lfCount := strings.Count(content, "\n") - crlfCount
	if crlfCount > lfCount {
		return "\r\n"
	}
	return "\n"
}

// normalizeQuotes normalizes curly/smart quotes to straight quotes.
func normalizeQuotes(s string) string {
	replacer := strings.NewReplacer(
		"\u2018", "'", // left single
		"\u2019", "'", // right single
		"\u201C", "\"", // left double
		"\u201D", "\"", // right double
		"\u2013", "-", // en dash
		"\u2014", "-", // em dash
	)
	return replacer.Replace(s)
}
