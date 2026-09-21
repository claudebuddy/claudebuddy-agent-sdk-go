package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

const (
	defaultReadLimit = 2000
	maxReadLimit     = 100000
	maxFileSize      = 1 << 30 // 1 GiB
)

// FileReadTool reads files from the filesystem with support for text, images,
// PDFs, and Jupyter notebooks.
type FileReadTool struct{}

func NewFileReadTool() *FileReadTool { return &FileReadTool{} }

func (t *FileReadTool) Name() string { return "Read" }

func (t *FileReadTool) Description() string {
	return `Reads a file from the local filesystem. You can access any file directly by using this tool.

Usage:
- The file_path parameter must be an absolute path, not a relative path
- By default, it reads up to 2000 lines starting from the beginning of the file
- When you already know which part of the file you need, only read that part
- Results are returned using cat -n format, with line numbers starting at 1
- This tool can read images (PNG, JPG, etc), returning visual content
- This tool can read PDF files (.pdf). For large PDFs, provide the pages parameter
- This tool can read Jupyter notebooks (.ipynb files)
- This tool can only read files, not directories. Use Bash with 'ls' for directories`
}

func (t *FileReadTool) InputSchema() types.ToolInputSchema {
	return types.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"file_path": map[string]interface{}{
				"type":        "string",
				"description": "The absolute path to the file to read",
			},
			"offset": map[string]interface{}{
				"type":        "number",
				"description": "The line number to start reading from (0-based)",
				"minimum":     0,
			},
			"limit": map[string]interface{}{
				"type":             "number",
				"description":      "The number of lines to read",
				"exclusiveMinimum": 0,
			},
			"pages": map[string]interface{}{
				"type":        "string",
				"description": "Page range for PDF files (e.g., \"1-5\", \"3\", \"10-20\")",
			},
		},
		Required: []string{"file_path"},
	}
}

func (t *FileReadTool) IsConcurrencySafe(input map[string]interface{}) bool { return true }
func (t *FileReadTool) IsReadOnly(input map[string]interface{}) bool        { return true }

func (t *FileReadTool) Call(ctx context.Context, input map[string]interface{}, tCtx *types.ToolUseContext) (*types.ToolResult, error) {
	filePath, _ := input["file_path"].(string)
	if filePath == "" {
		return errorResult("file_path is required"), nil
	}

	// Resolve relative paths
	if !filepath.IsAbs(filePath) && tCtx != nil && tCtx.WorkingDir != "" {
		filePath = filepath.Join(tCtx.WorkingDir, filePath)
	}

	// Block device files
	if isBlockedDevicePath(filePath) {
		return errorResult(fmt.Sprintf("Cannot read device file: %s", filePath)), nil
	}

	// Check file exists
	info, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return errorResult(fmt.Sprintf("File does not exist. Note: your current working directory is %s.", getWorkDir(tCtx))), nil
		}
		return errorResult(err.Error()), nil
	}

	if info.IsDir() {
		return errorResult(fmt.Sprintf("%s is a directory, not a file. Use Bash with 'ls' to list directory contents.", filePath)), nil
	}

	if info.Size() > maxFileSize {
		return errorResult(fmt.Sprintf("File is too large (%d bytes, max %d bytes)", info.Size(), maxFileSize)), nil
	}

	ext := strings.ToLower(filepath.Ext(filePath))

	// Route to appropriate reader
	switch {
	case isImageExtension(ext):
		return t.readImage(filePath, info)
	case ext == ".ipynb":
		return t.readNotebook(filePath)
	default:
		return t.readText(filePath, input, tCtx)
	}
}

// readText reads a text file with line numbers.
func (t *FileReadTool) readText(filePath string, input map[string]interface{}, tCtx *types.ToolUseContext) (*types.ToolResult, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	// Binary detection
	if isBinaryContent(data) {
		return errorResult(fmt.Sprintf("File appears to be binary. Use Bash with 'xxd' or 'file' to inspect binary files.")), nil
	}

	content := string(data)
	lines := strings.Split(content, "\n")
	totalLines := len(lines)

	// Apply offset and limit
	offset := 0
	if o, ok := input["offset"].(float64); ok {
		offset = int(o)
	}
	limit := defaultReadLimit
	if l, ok := input["limit"].(float64); ok {
		limit = int(l)
		if limit > maxReadLimit {
			limit = maxReadLimit
		}
	}

	if offset >= totalLines {
		return errorResult(fmt.Sprintf("Offset %d is beyond end of file (%d lines)", offset, totalLines)), nil
	}

	end := offset + limit
	if end > totalLines {
		end = totalLines
	}

	// Format with line numbers (cat -n style)
	var sb strings.Builder
	for i := offset; i < end; i++ {
		sb.WriteString(strconv.Itoa(i+1) + "\t" + lines[i] + "\n")
	}

	// Track file state for staleness detection
	tCtx.SetFileReadState(filePath, &types.FileReadState{
		Content:   content,
		Timestamp: time.Now().UnixMilli(),
		Offset:    offset,
		Limit:     limit,
	})

	result := sb.String()
	if end < totalLines {
		result += fmt.Sprintf("\n(showing lines %d-%d of %d total)", offset+1, end, totalLines)
	}

	// Empty file warning
	if strings.TrimSpace(content) == "" {
		result = "(empty file)"
	}

	return &types.ToolResult{
		Data: map[string]interface{}{
			"type": "text",
			"file": map[string]interface{}{
				"filePath":   filePath,
				"numLines":   end - offset,
				"startLine":  offset,
				"totalLines": totalLines,
			},
		},
		Content: []types.ContentBlock{{Type: types.ContentBlockText, Text: result}},
	}, nil
}

// readImage reads an image file and returns it as base64.
func (t *FileReadTool) readImage(filePath string, info os.FileInfo) (*types.ToolResult, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	// Detect MIME type
	mimeType := http.DetectContentType(data)
	if !strings.HasPrefix(mimeType, "image/") {
		mimeType = mimeTypeFromExt(filepath.Ext(filePath))
	}

	encoded := base64.StdEncoding.EncodeToString(data)

	return &types.ToolResult{
		Data: map[string]interface{}{
			"type": "image",
			"file": map[string]interface{}{
				"filePath":     filePath,
				"base64":       encoded,
				"mediaType":    mimeType,
				"originalSize": info.Size(),
			},
		},
		Content: []types.ContentBlock{{
			Type: types.ContentBlockImage,
			Source: &types.ImageSource{
				Type:      "base64",
				MediaType: mimeType,
				Data:      encoded,
			},
		}},
	}, nil
}

// readNotebook reads a Jupyter notebook file.
func (t *FileReadTool) readNotebook(filePath string) (*types.ToolResult, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return errorResult(err.Error()), nil
	}

	var notebook struct {
		Cells []struct {
			CellType string   `json:"cell_type"`
			Source   []string `json:"source"`
			Outputs  []struct {
				OutputType string                 `json:"output_type"`
				Text       []string               `json:"text,omitempty"`
				Data       map[string]interface{} `json:"data,omitempty"`
			} `json:"outputs,omitempty"`
		} `json:"cells"`
	}

	if err := json.Unmarshal(data, &notebook); err != nil {
		// Not valid notebook JSON, fall back to text reading
		return &types.ToolResult{
			Content: []types.ContentBlock{{
				Type: types.ContentBlockText,
				Text: string(data),
			}},
		}, nil
	}

	var sb strings.Builder
	for i, cell := range notebook.Cells {
		sb.WriteString(fmt.Sprintf("--- Cell %d (%s) ---\n", i+1, cell.CellType))
		sb.WriteString(strings.Join(cell.Source, ""))
		sb.WriteString("\n")

		// Include outputs
		for _, output := range cell.Outputs {
			if len(output.Text) > 0 {
				sb.WriteString("\n[Output]\n")
				sb.WriteString(strings.Join(output.Text, ""))
				sb.WriteString("\n")
			}
			if textData, ok := output.Data["text/plain"]; ok {
				if lines, ok := textData.([]interface{}); ok {
					sb.WriteString("\n[Output]\n")
					for _, line := range lines {
						sb.WriteString(fmt.Sprintf("%v", line))
					}
					sb.WriteString("\n")
				}
			}
		}
		sb.WriteString("\n")
	}

	return &types.ToolResult{
		Data: map[string]interface{}{
			"type": "notebook",
			"file": map[string]interface{}{
				"filePath": filePath,
				"numCells": len(notebook.Cells),
			},
		},
		Content: []types.ContentBlock{{Type: types.ContentBlockText, Text: sb.String()}},
	}, nil
}

// --- Helper functions ---

func isImageExtension(ext string) bool {
	imageExts := map[string]bool{
		".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
		".bmp": true, ".webp": true, ".svg": true, ".ico": true,
		".tiff": true, ".tif": true,
	}
	return imageExts[ext]
}

func mimeTypeFromExt(ext string) string {
	mimeTypes := map[string]string{
		".png":  "image/png",
		".jpg":  "image/jpeg",
		".jpeg": "image/jpeg",
		".gif":  "image/gif",
		".bmp":  "image/bmp",
		".webp": "image/webp",
		".svg":  "image/svg+xml",
		".ico":  "image/x-icon",
		".tiff": "image/tiff",
		".tif":  "image/tiff",
	}
	if mt, ok := mimeTypes[strings.ToLower(ext)]; ok {
		return mt
	}
	return "application/octet-stream"
}

// isBinaryContent checks if content appears to be binary (non-UTF8 or contains null bytes).
func isBinaryContent(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	// Check first 8KB for null bytes or invalid UTF-8
	checkLen := len(data)
	if checkLen > 8192 {
		checkLen = 8192
	}
	sample := data[:checkLen]

	if bytes.ContainsRune(sample, 0) {
		return true
	}
	if !utf8.Valid(sample) {
		return true
	}
	return false
}

func isBlockedDevicePath(path string) bool {
	blockedPrefixes := []string{
		"/dev/zero", "/dev/null", "/dev/random", "/dev/urandom",
		"/dev/stdin", "/dev/stdout", "/dev/stderr",
		"/dev/fd/", "/dev/sd", "/dev/disk",
		"/proc/kcore",
	}
	for _, prefix := range blockedPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func getWorkDir(tCtx *types.ToolUseContext) string {
	if tCtx != nil && tCtx.WorkingDir != "" {
		return tCtx.WorkingDir
	}
	wd, _ := os.Getwd()
	return wd
}
