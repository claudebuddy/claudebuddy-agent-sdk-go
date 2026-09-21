package plugins

import (
	"os"
	"path/filepath"
	"testing"
)

func setupPluginDir(t *testing.T, manifest string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0644); err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}
	return dir
}

func TestNewManager(t *testing.T) {
	m := NewManager()
	if m == nil {
		t.Fatal("expected non-nil manager")
	}
	if len(m.List()) != 0 {
		t.Error("expected empty plugin list")
	}
}

func TestLoadPlugin(t *testing.T) {
	dir := setupPluginDir(t, `{
		"name": "test-plugin",
		"version": "1.0.0",
		"description": "A test plugin",
		"capabilities": ["tool"]
	}`)

	m := NewManager()
	p, err := m.Load(PluginConfig{Type: "local", Path: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name != "test-plugin" {
		t.Errorf("expected name %q, got %q", "test-plugin", p.Name)
	}
	if p.Version != "1.0.0" {
		t.Errorf("expected version %q, got %q", "1.0.0", p.Version)
	}
	if !p.Loaded {
		t.Error("expected Loaded to be true")
	}
	if len(p.Capabilities) != 1 || p.Capabilities[0] != "tool" {
		t.Errorf("unexpected capabilities: %v", p.Capabilities)
	}
}

func TestLoadPluginUnsupportedType(t *testing.T) {
	m := NewManager()
	_, err := m.Load(PluginConfig{Type: "remote", Path: "/tmp"})
	if err == nil {
		t.Error("expected error for unsupported type")
	}
}

func TestLoadPluginMissingManifest(t *testing.T) {
	m := NewManager()
	_, err := m.Load(PluginConfig{Type: "local", Path: t.TempDir()})
	if err == nil {
		t.Error("expected error for missing manifest")
	}
}

func TestLoadPluginMissingName(t *testing.T) {
	dir := setupPluginDir(t, `{"version": "1.0"}`)
	m := NewManager()
	_, err := m.Load(PluginConfig{Type: "local", Path: dir})
	if err == nil {
		t.Error("expected error for missing name")
	}
}

func TestLoadPluginInvalidJSON(t *testing.T) {
	dir := setupPluginDir(t, `not json`)
	m := NewManager()
	_, err := m.Load(PluginConfig{Type: "local", Path: dir})
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestGetPlugin(t *testing.T) {
	dir := setupPluginDir(t, `{"name": "foo"}`)
	m := NewManager()
	m.Load(PluginConfig{Type: "local", Path: dir})

	p := m.Get("foo")
	if p == nil {
		t.Fatal("expected plugin")
	}
	if p.Name != "foo" {
		t.Errorf("expected name %q, got %q", "foo", p.Name)
	}

	if m.Get("nonexistent") != nil {
		t.Error("expected nil for nonexistent plugin")
	}
}

func TestUnloadPlugin(t *testing.T) {
	dir := setupPluginDir(t, `{"name": "bar"}`)
	m := NewManager()
	m.Load(PluginConfig{Type: "local", Path: dir})

	if err := m.Unload("bar"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m.List()) != 0 {
		t.Error("expected empty list after unload")
	}
}

func TestUnloadNotFound(t *testing.T) {
	m := NewManager()
	if err := m.Unload("missing"); err == nil {
		t.Error("expected error for missing plugin")
	}
}

func TestListPlugins(t *testing.T) {
	m := NewManager()
	dir1 := setupPluginDir(t, `{"name": "a"}`)
	dir2 := setupPluginDir(t, `{"name": "b"}`)
	m.Load(PluginConfig{Type: "local", Path: dir1})
	m.Load(PluginConfig{Type: "local", Path: dir2})

	plugins := m.List()
	if len(plugins) != 2 {
		t.Errorf("expected 2 plugins, got %d", len(plugins))
	}
}
