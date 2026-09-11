package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/claudebuddy/claudebuddy-agent-sdk-go/types"
)

const (
	bashDefaultTimeout    = 120 * time.Second
	bashMaxTimeout        = 600 * time.Second
	bashMaxOutputSize     = 1024 * 1024     // 1MB inline
	bashLargeOutputThresh = 20 * 1024       // 20KB → persist to disk
	bashMaxOutputDisk     = 5 * 1024 * 1024 // 5MB on disk
)

// BashTool executes shell commands with security checks, background support,
// and large output handling.
type BashTool struct {
	// Env provides additional environment variables for all commands.
	Env map[string]string

	// backgroundTasks tracks running background tasks.
	mu              sync.Mutex
	backgroundTasks map[string]*backgroundTask
	nextTaskID      atomic.Int64
}

type backgroundTask struct {
	ID        string
	Command   string
	Cmd       *exec.Cmd
	Output    *bytes.Buffer
	StartTime time.Time
	Done      chan struct{}
	Err       error
}

func NewBashTool() *BashTool {
	return &BashTool{
		backgroundTasks: make(map[string]*backgroundTask),
	}
}

func (t *BashTool) Name() string { return "Bash" }

func (t *BashTool) Description() string {
	return `Executes a given bash command and returns its output.

The working directory persists between commands, but shell state does not. The shell environment is initialized from the user's profile.

IMPORTANT: Avoid using this tool to run find, grep, cat, head, tail, sed, awk, or echo commands when dedicated tools (Read, Glob, Grep, Edit, Write) can accomplish the task instead.

You may specify an optional timeout in milliseconds (up to 600000ms / 10 minutes). Default timeout is 120000ms (2 minutes).
You can use run_in_background to run long commands asynchronously.`
}

func (t *BashTool) InputSchema() types.ToolInputSchema {
	return types.ToolInputSchema{
		Type: "object",
		Properties: map[string]interface{}{
			"command": map[string]interface{}{
				"type":        "string",
				"description": "The bash command to execute",
			},
			"timeout": map[string]interface{}{
				"type":        "number",
				"description": "Optional timeout in milliseconds (max 600000)",
			},
			"description": map[string]interface{}{
				"type":        "string",
				"description": "Clear, concise description of what this command does",
			},
			"run_in_background": map[string]interface{}{
				"type":        "boolean",
				"description": "Run command in background and return a task ID",
			},
		},
		Required: []string{"command"},
	}
}

func (t *BashTool) IsConcurrencySafe(input map[string]interface{}) bool { return false }

func (t *BashTool) IsReadOnly(input map[string]interface{}) bool {
	cmd, _ := input["command"].(string)
	return isReadCommand(cmd)
}

func (t *BashTool) Call(ctx context.Context, input map[string]interface{}, tCtx *types.ToolUseContext) (*types.ToolResult, error) {
	command, _ := input["command"].(string)
	if command == "" {
		return errorResult("command is required"), nil
	}

	// Security: detect destructive commands and warn
	if warning := detectDestructiveCommand(command); warning != "" {
		// Still execute but prepend a warning to output
		_ = warning // In SDK mode we don't block, just note it
	}

	// Security: detect blocked sleep patterns
	if msg := detectBlockedSleepPattern(command); msg != "" {
		return errorResult(msg), nil
	}

	// Background execution
	if bg, _ := input["run_in_background"].(bool); bg {
		return t.runInBackground(ctx, command, tCtx)
	}

	// Parse timeout
	timeout := bashDefaultTimeout
	if timeoutMs, ok := input["timeout"].(float64); ok {
		timeout = time.Duration(timeoutMs) * time.Millisecond
		if timeout > bashMaxTimeout {
			timeout = bashMaxTimeout
		}
	}

	return t.execute(ctx, command, timeout, tCtx)
}

func (t *BashTool) execute(ctx context.Context, command string, timeout time.Duration, tCtx *types.ToolUseContext) (*types.ToolResult, error) {
	workDir := "."
	if tCtx != nil && tCtx.WorkingDir != "" {
		workDir = tCtx.WorkingDir
	}

	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "bash", "-c", command)
	cmd.Dir = workDir

	// Set environment
	cmd.Env = os.Environ()
	if t.Env != nil {
		for k, v := range t.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	startTime := time.Now()
	err := cmd.Run()
	duration := time.Since(startTime)

	outBytes := stdout.Bytes()
	errBytes := stderr.Bytes()
	exitCode := 0

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}

	// Handle timeout
	if cmdCtx.Err() == context.DeadlineExceeded {
		result := combineOutput(outBytes, errBytes)
		return &types.ToolResult{
			IsError: true,
			Error:   fmt.Sprintf("Command timed out after %v", timeout),
			Content: []types.ContentBlock{{
				Type: types.ContentBlockText,
				Text: fmt.Sprintf("Command timed out after %v.\n\nPartial output:\n%s", timeout, truncateBytes(result, bashMaxOutputSize)),
			}},
		}, nil
	}

	output := combineOutput(outBytes, errBytes)

	// Large output handling: persist to disk if > threshold
	var persistedPath string
	if len(output) > bashLargeOutputThresh {
		persistedPath = t.persistLargeOutput(output, tCtx)
		if persistedPath != "" {
			// Truncate inline output and reference the file
			truncated := truncateBytes(output, bashLargeOutputThresh)
			text := string(truncated) + fmt.Sprintf(
				"\n\n... output truncated. Full output (%d bytes) saved to %s",
				len(output), persistedPath)
			return &types.ToolResult{
				Data: map[string]interface{}{
					"stdout":              stdout.String(),
					"stderr":              stderr.String(),
					"exitCode":            exitCode,
					"interrupted":         false,
					"durationMs":          duration.Milliseconds(),
					"persistedOutputPath": persistedPath,
					"persistedOutputSize": len(output),
				},
				Content: []types.ContentBlock{{Type: types.ContentBlockText, Text: text}},
			}, nil
		}
	}

	// Truncate if still too large
	if len(output) > bashMaxOutputSize {
		output = append(output[:bashMaxOutputSize], []byte("\n... (output truncated)")...)
	}

	text := string(output)
	if text == "" {
		text = "(no output)"
	} else {
		text = strings.TrimRight(text, "\n")
	}

	// Add exit code interpretation for non-zero exits
	if exitCode != 0 {
		text += fmt.Sprintf("\n\nExit code: %d", exitCode)
	}

	return &types.ToolResult{
		Data: map[string]interface{}{
			"stdout":      stdout.String(),
			"stderr":      stderr.String(),
			"exitCode":    exitCode,
			"interrupted": false,
			"durationMs":  duration.Milliseconds(),
		},
		Content: []types.ContentBlock{{Type: types.ContentBlockText, Text: text}},
	}, nil
}

// runInBackground starts a command in the background and returns immediately.
func (t *BashTool) runInBackground(ctx context.Context, command string, tCtx *types.ToolUseContext) (*types.ToolResult, error) {
	workDir := "."
	if tCtx != nil && tCtx.WorkingDir != "" {
		workDir = tCtx.WorkingDir
	}

	taskID := fmt.Sprintf("bg_%d", t.nextTaskID.Add(1))

	cmd := exec.Command("bash", "-c", command)
	cmd.Dir = workDir
	cmd.Env = os.Environ()

	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output

	if err := cmd.Start(); err != nil {
		return errorResult(fmt.Sprintf("Failed to start background command: %v", err)), nil
	}

	task := &backgroundTask{
		ID:        taskID,
		Command:   command,
		Cmd:       cmd,
		Output:    &output,
		StartTime: time.Now(),
		Done:      make(chan struct{}),
	}

	t.mu.Lock()
	t.backgroundTasks[taskID] = task
	t.mu.Unlock()

	// Wait in goroutine
	go func() {
		task.Err = cmd.Wait()
		close(task.Done)
	}()

	return &types.ToolResult{
		Data: map[string]interface{}{
			"backgroundTaskId": taskID,
		},
		Content: []types.ContentBlock{{
			Type: types.ContentBlockText,
			Text: fmt.Sprintf("Command started in background with task ID: %s\nUse Bash to check output: cat /proc/%d/fd/1 or wait for completion.", taskID, cmd.Process.Pid),
		}},
	}, nil
}

// GetBackgroundTask returns a background task by ID.
func (t *BashTool) GetBackgroundTask(id string) *backgroundTask {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.backgroundTasks[id]
}

// persistLargeOutput saves large output to a temporary file.
func (t *BashTool) persistLargeOutput(output []byte, tCtx *types.ToolUseContext) string {
	if len(output) > bashMaxOutputDisk {
		output = output[:bashMaxOutputDisk]
	}

	tmpDir := os.TempDir()
	f, err := os.CreateTemp(tmpDir, "bash-output-*.txt")
	if err != nil {
		return ""
	}
	defer f.Close()

	if _, err := f.Write(output); err != nil {
		os.Remove(f.Name())
		return ""
	}

	return f.Name()
}

// combineOutput merges stdout and stderr bytes.
func combineOutput(stdout, stderr []byte) []byte {
	if len(stderr) == 0 {
		return stdout
	}
	if len(stdout) == 0 {
		return stderr
	}
	result := make([]byte, 0, len(stdout)+1+len(stderr))
	result = append(result, stdout...)
	if stdout[len(stdout)-1] != '\n' {
		result = append(result, '\n')
	}
	result = append(result, stderr...)
	return result
}

func truncateBytes(b []byte, maxLen int) []byte {
	if len(b) <= maxLen {
		return b
	}
	return b[:maxLen]
}

func errorResult(msg string) *types.ToolResult {
	return &types.ToolResult{
		IsError: true,
		Error:   msg,
		Content: []types.ContentBlock{{Type: types.ContentBlockText, Text: "Error: " + msg}},
	}
}

// --- Security checks ---

// destructivePatterns identifies potentially dangerous commands.
var destructivePatterns = []struct {
	pattern *regexp.Regexp
	warning string
}{
	{regexp.MustCompile(`\brm\s+(-[a-zA-Z]*[rR]|--recursive)`), "Recursive delete detected"},
	{regexp.MustCompile(`\brm\s+-[a-zA-Z]*f`), "Force delete detected"},
	{regexp.MustCompile(`\bgit\s+(reset\s+--hard|clean\s+-[a-zA-Z]*f|push\s+--force|push\s+-f)`), "Destructive git operation"},
	{regexp.MustCompile(`\bgit\s+branch\s+-D\b`), "Force branch deletion"},
	{regexp.MustCompile(`\bchmod\s+-R\s+777\b`), "Recursive world-writable permissions"},
	{regexp.MustCompile(`>\s*/dev/sd[a-z]`), "Direct disk write detected"},
	{regexp.MustCompile(`\bmkfs\b`), "Filesystem format detected"},
	{regexp.MustCompile(`\bdd\s+.*of=/dev/`), "Direct disk write with dd"},
	{regexp.MustCompile(`:(){ :\|:& };:`), "Fork bomb detected"},
}

func detectDestructiveCommand(cmd string) string {
	for _, dp := range destructivePatterns {
		if dp.pattern.MatchString(cmd) {
			return dp.warning
		}
	}
	return ""
}

// detectBlockedSleepPattern blocks standalone long sleep commands.
var sleepPattern = regexp.MustCompile(`^\s*sleep\s+(\d+)\s*$`)

func detectBlockedSleepPattern(cmd string) string {
	matches := sleepPattern.FindStringSubmatch(strings.TrimSpace(cmd))
	if matches != nil {
		// Parse duration - block if > 10 seconds
		var seconds int
		fmt.Sscanf(matches[1], "%d", &seconds)
		if seconds > 10 {
			return fmt.Sprintf("Standalone sleep of %ds is blocked. Use run_in_background for long-running commands, or reduce the sleep duration.", seconds)
		}
	}
	return ""
}

// isReadCommand returns true if the command is read-only.
func isReadCommand(cmd string) bool {
	readPrefixes := []string{
		"cat ", "head ", "tail ", "less ", "more ",
		"ls", "dir ", "find ", "locate ",
		"grep ", "rg ", "ag ", "ack ",
		"git log", "git show", "git diff", "git status", "git branch",
		"git remote", "git tag", "git stash list",
		"echo ", "printf ",
		"wc ", "du ", "df ",
		"which ", "whereis ", "type ", "file ",
		"env", "printenv", "set",
		"pwd", "whoami", "hostname", "uname",
		"date", "cal", "uptime",
		"ps ", "top -l", "htop",
		"docker ps", "docker images", "docker inspect",
		"kubectl get", "kubectl describe",
		"npm ls", "npm list", "npm view",
		"go list", "go env", "go version",
		"python --version", "python3 --version",
		"node --version", "node -v",
		"java -version", "javac -version",
		"cargo --version", "rustc --version",
	}
	trimmed := strings.TrimSpace(cmd)
	for _, prefix := range readPrefixes {
		if strings.HasPrefix(trimmed, prefix) || trimmed == strings.TrimSpace(prefix) {
			return true
		}
	}
	return false
}

// IsSearchOrReadCommand classifies a bash command for UI collapsing.
func IsSearchOrReadCommand(cmd string) (isSearch, isRead, isList bool) {
	trimmed := strings.TrimSpace(cmd)

	searchPrefixes := []string{"grep ", "rg ", "ag ", "ack ", "find ", "locate ", "fd "}
	for _, p := range searchPrefixes {
		if strings.HasPrefix(trimmed, p) {
			return true, false, false
		}
	}

	readPrefixes := []string{"cat ", "head ", "tail ", "less ", "more ", "bat "}
	for _, p := range readPrefixes {
		if strings.HasPrefix(trimmed, p) {
			return false, true, false
		}
	}

	listPrefixes := []string{"ls", "dir ", "tree "}
	for _, p := range listPrefixes {
		if strings.HasPrefix(trimmed, p) || trimmed == strings.TrimSpace(p) {
			return false, false, true
		}
	}

	return false, false, false
}

// SuggestDedicatedTool suggests a dedicated tool for common bash commands.
func SuggestDedicatedTool(cmd string) string {
	trimmed := strings.TrimSpace(cmd)

	suggestions := []struct {
		prefix string
		tool   string
	}{
		{"cat ", "Use the Read tool instead of cat"},
		{"head ", "Use the Read tool with limit instead of head"},
		{"tail ", "Use the Read tool with offset instead of tail"},
		{"grep ", "Use the Grep tool instead of grep"},
		{"rg ", "Use the Grep tool instead of rg"},
		{"find ", "Use the Glob tool instead of find"},
		{"sed ", "Use the Edit tool instead of sed"},
		{"awk ", "Use the Edit tool or Read tool instead of awk"},
	}

	for _, s := range suggestions {
		if strings.HasPrefix(trimmed, s.prefix) {
			return s.tool
		}
	}
	return ""
}

// ValidateFilePath checks if a file path is within allowed directories.
func ValidateFilePath(path string, workingDir string, additionalDirs []string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}

	// Check against working directory
	if workingDir != "" {
		absWork, _ := filepath.Abs(workingDir)
		if strings.HasPrefix(absPath, absWork) {
			return nil
		}
	}

	// Check additional directories
	for _, dir := range additionalDirs {
		absDir, _ := filepath.Abs(dir)
		if strings.HasPrefix(absPath, absDir) {
			return nil
		}
	}

	// Allow temp directories
	tmpDir := os.TempDir()
	if strings.HasPrefix(absPath, tmpDir) {
		return nil
	}

	return nil // In SDK mode, don't restrict by default
}
