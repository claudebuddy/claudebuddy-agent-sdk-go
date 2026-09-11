package checkpoint

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewManager(t *testing.T) {
	m := NewManager(true)
	if m == nil {
		t.Fatal("expected non-nil manager")
	}
	if !m.IsEnabled() {
		t.Error("expected enabled")
	}

	m2 := NewManager(false)
	if m2.IsEnabled() {
		t.Error("expected disabled")
	}
}

func TestTrackFileAndCreateCheckpoint(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	os.WriteFile(filePath, []byte("hello"), 0644)

	m := NewManager(true)
	m.TrackFile(filePath)

	if err := m.CreateCheckpoint("msg-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cps := m.ListCheckpoints()
	if len(cps) != 1 {
		t.Fatalf("expected 1 checkpoint, got %d", len(cps))
	}
	if cps[0].MessageID != "msg-1" {
		t.Errorf("expected messageID %q, got %q", "msg-1", cps[0].MessageID)
	}
	if len(cps[0].Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(cps[0].Files))
	}
	if !cps[0].Files[0].Exists {
		t.Error("expected file to exist")
	}
	if string(cps[0].Files[0].Content) != "hello" {
		t.Errorf("expected content %q, got %q", "hello", string(cps[0].Files[0].Content))
	}
}

func TestRewindTo(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "data.txt")
	os.WriteFile(filePath, []byte("v1"), 0644)

	m := NewManager(true)
	m.TrackFile(filePath)

	if err := m.CreateCheckpoint("cp-1"); err != nil {
		t.Fatal(err)
	}

	// Modify the file.
	os.WriteFile(filePath, []byte("v2"), 0644)

	// Verify it changed.
	content, _ := os.ReadFile(filePath)
	if string(content) != "v2" {
		t.Fatalf("expected v2, got %s", string(content))
	}

	// Rewind.
	if err := m.RewindTo("cp-1"); err != nil {
		t.Fatal(err)
	}

	content, _ = os.ReadFile(filePath)
	if string(content) != "v1" {
		t.Errorf("expected v1 after rewind, got %s", string(content))
	}
}

func TestRewindDeletesNewFile(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "new.txt")
	// File does not exist at checkpoint time.

	m := NewManager(true)
	m.TrackFile(filePath)

	if err := m.CreateCheckpoint("cp-before"); err != nil {
		t.Fatal(err)
	}

	// Create the file after checkpoint.
	os.WriteFile(filePath, []byte("created"), 0644)

	// Rewind should delete it.
	if err := m.RewindTo("cp-before"); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Error("expected file to be deleted after rewind")
	}
}

func TestRewindNotFound(t *testing.T) {
	m := NewManager(true)
	if err := m.RewindTo("nonexistent"); err == nil {
		t.Error("expected error for missing checkpoint")
	}
}

func TestCreateCheckpointDisabled(t *testing.T) {
	m := NewManager(false)
	if err := m.CreateCheckpoint("x"); err == nil {
		t.Error("expected error when disabled")
	}
}

func TestRewindDisabled(t *testing.T) {
	m := NewManager(false)
	if err := m.RewindTo("x"); err == nil {
		t.Error("expected error when disabled")
	}
}

func TestTrackNonexistentFile(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "ghost.txt")

	m := NewManager(true)
	m.TrackFile(filePath)

	if err := m.CreateCheckpoint("cp-ghost"); err != nil {
		t.Fatal(err)
	}

	cps := m.ListCheckpoints()
	if len(cps[0].Files) != 1 {
		t.Fatal("expected 1 file state")
	}
	if cps[0].Files[0].Exists {
		t.Error("expected file to not exist")
	}
}

func TestMultipleCheckpoints(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "multi.txt")
	os.WriteFile(filePath, []byte("a"), 0644)

	m := NewManager(true)
	m.TrackFile(filePath)

	m.CreateCheckpoint("cp-a")
	os.WriteFile(filePath, []byte("b"), 0644)
	m.CreateCheckpoint("cp-b")
	os.WriteFile(filePath, []byte("c"), 0644)

	// Rewind to cp-a.
	if err := m.RewindTo("cp-a"); err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(filePath)
	if string(content) != "a" {
		t.Errorf("expected a, got %s", string(content))
	}

	// Rewind to cp-b.
	if err := m.RewindTo("cp-b"); err != nil {
		t.Fatal(err)
	}
	content, _ = os.ReadFile(filePath)
	if string(content) != "b" {
		t.Errorf("expected b, got %s", string(content))
	}
}
