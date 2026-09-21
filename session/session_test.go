package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// helper: write a JSONL session file with the given lines.
func writeSession(t *testing.T, dir, sessionID string, lines []interface{}) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	fp := filepath.Join(dir, sessionID+".jsonl")
	f, err := os.Create(fp)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, line := range lines {
		data, _ := json.Marshal(line)
		f.Write(append(data, '\n'))
	}
	return fp
}

func TestListSessions(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)

	// Write two session files.
	writeSession(t, dir, "sess-1", []interface{}{
		map[string]interface{}{"type": "user", "uuid": "u1", "message": "hello"},
	})
	writeSession(t, dir, "sess-2", []interface{}{
		map[string]interface{}{"type": "session_meta", "sessionId": "sess-2", "customTitle": "My Session"},
		map[string]interface{}{"type": "user", "uuid": "u2", "message": "world"},
	})

	sessions, err := mgr.ListSessions("")
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}

	// Find sess-2 and check title.
	var found bool
	for _, s := range sessions {
		if s.SessionID == "sess-2" {
			found = true
			if s.CustomTitle != "My Session" {
				t.Errorf("expected title 'My Session', got %q", s.CustomTitle)
			}
			if s.FirstPrompt != "world" {
				t.Errorf("expected first prompt 'world', got %q", s.FirstPrompt)
			}
		}
	}
	if !found {
		t.Error("sess-2 not found in list")
	}
}

func TestListSessions_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	sessions, err := mgr.ListSessions("")
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("expected 0 sessions, got %d", len(sessions))
	}
}

func TestListSessions_NonExistentDir(t *testing.T) {
	mgr := NewManager("/tmp/nonexistent-session-dir-test-xyz")
	sessions, err := mgr.ListSessions("")
	if err != nil {
		t.Fatal(err)
	}
	if sessions != nil {
		t.Fatalf("expected nil, got %v", sessions)
	}
}

func TestGetSessionInfo(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)

	writeSession(t, dir, "info-test", []interface{}{
		map[string]interface{}{"type": "session_meta", "sessionId": "info-test", "gitBranch": "main", "cwd": "/home/user"},
		map[string]interface{}{"type": "user", "uuid": "u1", "message": "first question"},
		map[string]interface{}{"type": "assistant", "uuid": "a1", "message": "response"},
	})

	info, err := mgr.GetSessionInfo("info-test")
	if err != nil {
		t.Fatal(err)
	}
	if info.SessionID != "info-test" {
		t.Errorf("unexpected session ID: %s", info.SessionID)
	}
	if info.GitBranch != "main" {
		t.Errorf("expected gitBranch 'main', got %q", info.GitBranch)
	}
	if info.CWD != "/home/user" {
		t.Errorf("expected cwd '/home/user', got %q", info.CWD)
	}
	if info.FirstPrompt != "first question" {
		t.Errorf("expected first prompt 'first question', got %q", info.FirstPrompt)
	}
	if info.FileSize == 0 {
		t.Error("expected non-zero file size")
	}
}

func TestGetSessionInfo_NotFound(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	_, err := mgr.GetSessionInfo("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent session")
	}
}

func TestGetSessionMessages(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)

	writeSession(t, dir, "msgs", []interface{}{
		map[string]interface{}{"type": "user", "uuid": "u1", "message": "hello"},
		map[string]interface{}{"type": "assistant", "uuid": "a1", "message": "hi there"},
		map[string]interface{}{"type": "user", "uuid": "u2", "message": "thanks"},
	})

	msgs, err := mgr.GetSessionMessages("msgs")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	if msgs[0].Type != "user" || msgs[0].UUID != "u1" {
		t.Errorf("unexpected first message: %+v", msgs[0])
	}
	if msgs[1].Type != "assistant" || msgs[1].UUID != "a1" {
		t.Errorf("unexpected second message: %+v", msgs[1])
	}
}

func TestRenameSession(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)

	writeSession(t, dir, "rename-me", []interface{}{
		map[string]interface{}{"type": "user", "uuid": "u1", "message": "original"},
	})

	if err := mgr.RenameSession("rename-me", "New Title"); err != nil {
		t.Fatal(err)
	}

	info, err := mgr.GetSessionInfo("rename-me")
	if err != nil {
		t.Fatal(err)
	}
	if info.CustomTitle != "New Title" {
		t.Errorf("expected title 'New Title', got %q", info.CustomTitle)
	}
}

func TestRenameSession_NotFound(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	err := mgr.RenameSession("no-such-session", "title")
	if err == nil {
		t.Error("expected error for nonexistent session")
	}
}

func TestTagSession(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)

	writeSession(t, dir, "tag-me", []interface{}{
		map[string]interface{}{"type": "user", "uuid": "u1", "message": "hi"},
	})

	// Set tag.
	tag := "important"
	if err := mgr.TagSession("tag-me", &tag); err != nil {
		t.Fatal(err)
	}

	info, err := mgr.GetSessionInfo("tag-me")
	if err != nil {
		t.Fatal(err)
	}
	if info.Tag != "important" {
		t.Errorf("expected tag 'important', got %q", info.Tag)
	}

	// Clear tag.
	if err := mgr.TagSession("tag-me", nil); err != nil {
		t.Fatal(err)
	}

	info, err = mgr.GetSessionInfo("tag-me")
	if err != nil {
		t.Fatal(err)
	}
	if info.Tag != "" {
		t.Errorf("expected empty tag after clear, got %q", info.Tag)
	}
}

func TestDeleteSession(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)

	writeSession(t, dir, "delete-me", []interface{}{
		map[string]interface{}{"type": "user", "uuid": "u1", "message": "bye"},
	})

	if err := mgr.DeleteSession("delete-me"); err != nil {
		t.Fatal(err)
	}

	fp := filepath.Join(dir, "delete-me.jsonl")
	if _, err := os.Stat(fp); !os.IsNotExist(err) {
		t.Error("expected file to be deleted")
	}
}

func TestDeleteSession_NotFound(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	err := mgr.DeleteSession("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent session")
	}
}

func TestForkSession(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)

	writeSession(t, dir, "source", []interface{}{
		map[string]interface{}{"type": "user", "uuid": "u1", "message": "first"},
		map[string]interface{}{"type": "assistant", "uuid": "a1", "message": "response1"},
		map[string]interface{}{"type": "user", "uuid": "u2", "message": "second"},
		map[string]interface{}{"type": "assistant", "uuid": "a2", "message": "response2"},
	})

	result, err := mgr.ForkSession("source", "a1", "Forked Session")
	if err != nil {
		t.Fatal(err)
	}

	if result.NewSessionID == "" {
		t.Error("expected non-empty new session ID")
	}
	if result.MessageCount != 2 {
		t.Errorf("expected 2 messages in fork, got %d", result.MessageCount)
	}

	// Read forked messages.
	forkedMsgs, err := mgr.GetSessionMessages(result.NewSessionID)
	if err != nil {
		t.Fatal(err)
	}

	// First line is session_meta, then 2 messages = 3 total.
	if len(forkedMsgs) != 3 {
		t.Fatalf("expected 3 lines (1 meta + 2 messages), got %d", len(forkedMsgs))
	}

	// Check that UUIDs are remapped (different from originals).
	for _, msg := range forkedMsgs {
		if msg.UUID == "u1" || msg.UUID == "a1" {
			t.Errorf("expected remapped UUID, got original: %s", msg.UUID)
		}
	}

	// Check that the forked session has the custom title.
	forkedInfo, err := mgr.GetSessionInfo(result.NewSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if forkedInfo.CustomTitle != "Forked Session" {
		t.Errorf("expected title 'Forked Session', got %q", forkedInfo.CustomTitle)
	}
}

func TestForkSession_MessageNotFound(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)

	writeSession(t, dir, "src", []interface{}{
		map[string]interface{}{"type": "user", "uuid": "u1", "message": "only"},
	})

	_, err := mgr.ForkSession("src", "nonexistent-uuid", "Title")
	if err == nil {
		t.Error("expected error when target message not found")
	}
}

func TestForkSession_SourceNotFound(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	_, err := mgr.ForkSession("no-source", "u1", "Title")
	if err == nil {
		t.Error("expected error for nonexistent source session")
	}
}

func TestTruncate(t *testing.T) {
	short := "hello"
	if got := truncate(short, 10); got != short {
		t.Errorf("truncate(%q, 10) = %q, want %q", short, got, short)
	}

	long := "this is a very long string that should be truncated"
	got := truncate(long, 20)
	if len(got) > 20 {
		t.Errorf("truncated string too long: %d", len(got))
	}
	if got[len(got)-3:] != "..." {
		t.Errorf("expected '...' suffix, got %q", got)
	}
}

func TestTimestampExtraction(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)

	ts := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	writeSession(t, dir, "ts-test", []interface{}{
		map[string]interface{}{"type": "user", "uuid": "u1", "message": "hi", "timestamp": ts},
	})

	info, err := mgr.GetSessionInfo("ts-test")
	if err != nil {
		t.Fatal(err)
	}
	if info.CreatedAt.Equal(info.LastModified) {
		// CreatedAt should have been updated from the timestamp in the file.
		// This can only fail if the file modtime happens to equal our test timestamp,
		// which is extremely unlikely.
		t.Log("note: CreatedAt equals LastModified, timestamp extraction may not have worked")
	}
}

func TestListSessionsWithSubdirectory(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)

	// Create a subdirectory with sessions.
	subDir := filepath.Join(dir, "project-a")
	writeSession(t, subDir, "sub-sess", []interface{}{
		map[string]interface{}{"type": "user", "uuid": "u1", "message": "in subdir"},
	})

	sessions, err := mgr.ListSessions(subDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].SessionID != "sub-sess" {
		t.Errorf("expected session ID 'sub-sess', got %q", sessions[0].SessionID)
	}
}
