package checkpoint

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// FileState captures the state of a single file at a point in time.
type FileState struct {
	Path    string    `json:"path"`
	Content []byte    `json:"content"`
	ModTime time.Time `json:"modTime"`
	Exists  bool      `json:"exists"`
}

// Checkpoint is a snapshot of tracked files at a specific point.
type Checkpoint struct {
	MessageID string      `json:"messageId"`
	Timestamp time.Time   `json:"timestamp"`
	Files     []FileState `json:"files"`
}

// Manager manages file checkpointing and rewind operations.
type Manager struct {
	mu           sync.Mutex
	checkpoints  []Checkpoint
	trackedFiles map[string]bool
	enabled      bool
}

// NewManager creates a new checkpoint manager.
func NewManager(enabled bool) *Manager {
	return &Manager{
		checkpoints:  make([]Checkpoint, 0),
		trackedFiles: make(map[string]bool),
		enabled:      enabled,
	}
}

// IsEnabled returns whether checkpointing is enabled.
func (m *Manager) IsEnabled() bool {
	return m.enabled
}

// TrackFile adds a file path to the set of tracked files.
func (m *Manager) TrackFile(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.trackedFiles[path] = true
}

// CreateCheckpoint snapshots all tracked files and stores the checkpoint.
func (m *Manager) CreateCheckpoint(messageID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.enabled {
		return fmt.Errorf("checkpointing is disabled")
	}

	var files []FileState
	for path := range m.trackedFiles {
		state, err := captureFileState(path)
		if err != nil {
			return fmt.Errorf("failed to capture state for %s: %w", path, err)
		}
		files = append(files, state)
	}

	cp := Checkpoint{
		MessageID: messageID,
		Timestamp: time.Now(),
		Files:     files,
	}
	m.checkpoints = append(m.checkpoints, cp)
	return nil
}

// RewindTo restores all tracked files to the state captured in the checkpoint
// identified by messageID. It finds the most recent checkpoint with that ID.
func (m *Manager) RewindTo(messageID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.enabled {
		return fmt.Errorf("checkpointing is disabled")
	}

	// Find the checkpoint (search from most recent).
	var target *Checkpoint
	for i := len(m.checkpoints) - 1; i >= 0; i-- {
		if m.checkpoints[i].MessageID == messageID {
			target = &m.checkpoints[i]
			break
		}
	}

	if target == nil {
		return fmt.Errorf("checkpoint %q not found", messageID)
	}

	for _, fs := range target.Files {
		if err := restoreFileState(fs); err != nil {
			return fmt.Errorf("failed to restore %s: %w", fs.Path, err)
		}
	}

	return nil
}

// ListCheckpoints returns all stored checkpoints.
func (m *Manager) ListCheckpoints() []Checkpoint {
	m.mu.Lock()
	defer m.mu.Unlock()

	result := make([]Checkpoint, len(m.checkpoints))
	copy(result, m.checkpoints)
	return result
}

// captureFileState reads the current state of a file.
func captureFileState(path string) (FileState, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return FileState{Path: path, Exists: false}, nil
	}
	if err != nil {
		return FileState{}, err
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return FileState{}, err
	}

	return FileState{
		Path:    path,
		Content: content,
		ModTime: info.ModTime(),
		Exists:  true,
	}, nil
}

// restoreFileState writes a file back to its captured state.
func restoreFileState(fs FileState) error {
	if !fs.Exists {
		// If the file didn't exist at checkpoint time, remove it if it exists now.
		if err := os.Remove(fs.Path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}

	return os.WriteFile(fs.Path, fs.Content, 0644)
}
