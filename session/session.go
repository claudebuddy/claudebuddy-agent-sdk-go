package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// SessionInfo holds metadata about a session.
type SessionInfo struct {
	SessionID    string    `json:"sessionId"`
	Summary      string    `json:"summary,omitempty"`
	LastModified time.Time `json:"lastModified"`
	FileSize     int64     `json:"fileSize"`
	CustomTitle  string    `json:"customTitle,omitempty"`
	FirstPrompt  string    `json:"firstPrompt,omitempty"`
	GitBranch    string    `json:"gitBranch,omitempty"`
	CWD          string    `json:"cwd,omitempty"`
	Tag          string    `json:"tag,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
}

// SessionMessage represents a single message line in a JSONL session file.
type SessionMessage struct {
	Type      string          `json:"type"`
	UUID      string          `json:"uuid"`
	SessionID string          `json:"sessionId,omitempty"`
	Message   json.RawMessage `json:"message,omitempty"`
}

// ForkResult holds the result of a fork operation.
type ForkResult struct {
	NewSessionID string `json:"newSessionId"`
	MessageCount int    `json:"messageCount"`
}

// sessionMeta is an internal envelope used when reading/writing session-level
// metadata lines (type "session_meta") inside a JSONL session file.
type sessionMeta struct {
	Type        string `json:"type"`
	SessionID   string `json:"sessionId,omitempty"`
	CustomTitle string `json:"customTitle,omitempty"`
	Tag         string `json:"tag"`
	GitBranch   string `json:"gitBranch,omitempty"`
	CWD         string `json:"cwd,omitempty"`
}

// Manager manages sessions stored as JSONL files.
type Manager struct {
	mu      sync.Mutex
	baseDir string // default ~/.claude/projects/
}

// NewManager creates a new session Manager.
// If baseDir is empty it defaults to ~/.claude/projects/.
func NewManager(baseDir string) *Manager {
	if baseDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		baseDir = filepath.Join(home, ".claude", "projects")
	}
	return &Manager{baseDir: baseDir}
}

// BaseDir returns the configured base directory.
func (m *Manager) BaseDir() string {
	return m.baseDir
}

// sessionFilePath returns the full path for a session ID.
// Session IDs map to <baseDir>/<sessionID>.jsonl
func (m *Manager) sessionFilePath(sessionID string) string {
	return filepath.Join(m.baseDir, sessionID+".jsonl")
}

// ListSessions scans directory for JSONL session files and returns metadata
// for each. If directory is empty, the base directory is used.
func (m *Manager) ListSessions(directory string) ([]SessionInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	dir := directory
	if dir == "" {
		dir = m.baseDir
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading directory %s: %w", dir, err)
	}

	var sessions []SessionInfo
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}

		sessionID := strings.TrimSuffix(entry.Name(), ".jsonl")
		filePath := filepath.Join(dir, entry.Name())

		info, err := m.readSessionInfo(filePath, sessionID)
		if err != nil {
			// Skip unreadable files.
			continue
		}
		sessions = append(sessions, *info)
	}

	return sessions, nil
}

// GetSessionInfo returns metadata for a single session.
func (m *Manager) GetSessionInfo(sessionID string) (*SessionInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	filePath := m.sessionFilePath(sessionID)
	return m.readSessionInfo(filePath, sessionID)
}

// GetSessionMessages reads all messages from a session file.
func (m *Manager) GetSessionMessages(sessionID string) ([]SessionMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	filePath := m.sessionFilePath(sessionID)
	return m.readAllMessages(filePath)
}

// RenameSession updates the custom title of a session by appending a
// metadata line to the session file.
func (m *Manager) RenameSession(sessionID, title string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	filePath := m.sessionFilePath(sessionID)
	if _, err := os.Stat(filePath); err != nil {
		return fmt.Errorf("session %s not found: %w", sessionID, err)
	}

	meta := sessionMeta{
		Type:        "session_meta",
		SessionID:   sessionID,
		CustomTitle: title,
	}
	return m.appendLine(filePath, meta)
}

// TagSession sets or clears a tag on a session. Pass nil to clear.
func (m *Manager) TagSession(sessionID string, tag *string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	filePath := m.sessionFilePath(sessionID)
	if _, err := os.Stat(filePath); err != nil {
		return fmt.Errorf("session %s not found: %w", sessionID, err)
	}

	tagValue := ""
	if tag != nil {
		tagValue = *tag
	}

	meta := sessionMeta{
		Type:      "session_meta",
		SessionID: sessionID,
		Tag:       tagValue,
	}
	return m.appendLine(filePath, meta)
}

// DeleteSession removes a session file.
func (m *Manager) DeleteSession(sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	filePath := m.sessionFilePath(sessionID)
	if err := os.Remove(filePath); err != nil {
		return fmt.Errorf("deleting session %s: %w", sessionID, err)
	}
	return nil
}

// ForkSession creates a new session from an existing one, preserving messages
// up to (and including) upToMessageID. UUIDs are remapped to new values.
func (m *Manager) ForkSession(sessionID string, upToMessageID string, newTitle string) (*ForkResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	filePath := m.sessionFilePath(sessionID)
	messages, err := m.readAllMessages(filePath)
	if err != nil {
		return nil, fmt.Errorf("reading source session: %w", err)
	}

	// Collect messages up to the target.
	var forked []SessionMessage
	found := false
	for _, msg := range messages {
		forked = append(forked, msg)
		if msg.UUID == upToMessageID {
			found = true
			break
		}
	}

	if !found {
		return nil, fmt.Errorf("message %s not found in session %s", upToMessageID, sessionID)
	}

	newSessionID := uuid.New().String()
	newFilePath := m.sessionFilePath(newSessionID)

	// Ensure directory exists.
	if err := os.MkdirAll(filepath.Dir(newFilePath), 0755); err != nil {
		return nil, fmt.Errorf("creating directory: %w", err)
	}

	file, err := os.Create(newFilePath)
	if err != nil {
		return nil, fmt.Errorf("creating fork file: %w", err)
	}
	defer file.Close()

	// Write a metadata line first if a title is provided.
	if newTitle != "" {
		meta := sessionMeta{
			Type:        "session_meta",
			SessionID:   newSessionID,
			CustomTitle: newTitle,
		}
		data, err := json.Marshal(meta)
		if err != nil {
			return nil, err
		}
		file.Write(append(data, '\n'))
	}

	// Remap UUIDs and write.
	uuidMap := make(map[string]string)
	for i := range forked {
		oldUUID := forked[i].UUID
		newUUID := uuid.New().String()
		uuidMap[oldUUID] = newUUID
		forked[i].UUID = newUUID
		forked[i].SessionID = newSessionID

		data, err := json.Marshal(forked[i])
		if err != nil {
			continue
		}
		file.Write(append(data, '\n'))
	}

	return &ForkResult{
		NewSessionID: newSessionID,
		MessageCount: len(forked),
	}, nil
}

// ---------- internal helpers ----------

// readSessionInfo extracts metadata from a JSONL session file by reading the
// first few and last few lines.
func (m *Manager) readSessionInfo(filePath, sessionID string) (*SessionInfo, error) {
	stat, err := os.Stat(filePath)
	if err != nil {
		return nil, err
	}

	info := &SessionInfo{
		SessionID:    sessionID,
		LastModified: stat.ModTime(),
		FileSize:     stat.Size(),
		CreatedAt:    stat.ModTime(), // will be overridden if we find a timestamp
	}

	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// Read head lines (up to 20) to find metadata and first prompt.
	headLines := readHeadLines(file, 20)
	for _, line := range headLines {
		m.extractInfoFromLine(line, info)
	}

	// Read tail lines (up to 10) for latest metadata updates.
	tailLines := readTailLines(filePath, 10)
	for _, line := range tailLines {
		m.extractInfoFromLine(line, info)
	}

	return info, nil
}

// extractInfoFromLine updates SessionInfo fields from a JSONL line.
func (m *Manager) extractInfoFromLine(line []byte, info *SessionInfo) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(line, &raw); err != nil {
		return
	}

	var msgType string
	if t, ok := raw["type"]; ok {
		json.Unmarshal(t, &msgType)
	}

	switch msgType {
	case "session_meta":
		var meta sessionMeta
		if err := json.Unmarshal(line, &meta); err == nil {
			if meta.CustomTitle != "" {
				info.CustomTitle = meta.CustomTitle
			}
			// Tag is always applied when the key is present in the JSON,
			// allowing an empty string to clear a previously set tag.
			if _, hasTag := raw["tag"]; hasTag {
				info.Tag = meta.Tag
			}
			if meta.GitBranch != "" {
				info.GitBranch = meta.GitBranch
			}
			if meta.CWD != "" {
				info.CWD = meta.CWD
			}
		}

	case "user":
		// Use first user message as the first prompt / summary.
		if info.FirstPrompt == "" {
			if msgRaw, ok := raw["message"]; ok {
				var text string
				if err := json.Unmarshal(msgRaw, &text); err == nil && text != "" {
					info.FirstPrompt = text
					if info.Summary == "" {
						info.Summary = truncate(text, 120)
					}
				}
			}
		}
	}

	// Try to extract a timestamp for CreatedAt from the first line that has one.
	if ts, ok := raw["timestamp"]; ok {
		var t time.Time
		if err := json.Unmarshal(ts, &t); err == nil && !t.IsZero() {
			if info.CreatedAt.Equal(info.LastModified) || t.Before(info.CreatedAt) {
				info.CreatedAt = t
			}
		}
	}
}

// readAllMessages reads every line of a JSONL session file into SessionMessage
// slices.
func (m *Manager) readAllMessages(filePath string) ([]SessionMessage, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var messages []SessionMessage
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg SessionMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		// Keep the full raw line as Message if not already set.
		if msg.Message == nil {
			msg.Message = json.RawMessage(append([]byte(nil), line...))
		}
		messages = append(messages, msg)
	}

	if err := scanner.Err(); err != nil {
		return messages, err
	}
	return messages, nil
}

// appendLine marshals v to JSON and appends it as a new line to the file.
func (m *Manager) appendLine(filePath string, v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}

	file, err := os.OpenFile(filePath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = file.Write(append(data, '\n'))
	return err
}

// readHeadLines reads up to n lines from the beginning of a file.
func readHeadLines(r io.ReadSeeker, n int) [][]byte {
	r.Seek(0, io.SeekStart)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	var lines [][]byte
	for scanner.Scan() && len(lines) < n {
		lines = append(lines, append([]byte(nil), scanner.Bytes()...))
	}
	return lines
}

// readTailLines reads the last n lines of a file by reading from the end.
func readTailLines(filePath string, n int) [][]byte {
	data, err := os.ReadFile(filePath)
	if err != nil || len(data) == 0 {
		return nil
	}

	// Split into lines and take the last n.
	var lines [][]byte
	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			line := data[start:i]
			if len(line) > 0 {
				lines = append(lines, line)
			}
			start = i + 1
		}
	}
	// Handle last line without trailing newline.
	if start < len(data) {
		lines = append(lines, data[start:])
	}

	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// truncate shortens a string to maxLen, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
