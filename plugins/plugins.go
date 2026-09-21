package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// PluginConfig describes how to load a plugin.
type PluginConfig struct {
	Type string `json:"type"` // "local"
	Path string `json:"path"`
}

// pluginManifest is the structure of a plugin.json file.
type pluginManifest struct {
	Name         string   `json:"name"`
	Version      string   `json:"version,omitempty"`
	Description  string   `json:"description,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

// Plugin represents a loaded plugin.
type Plugin struct {
	Name         string       `json:"name"`
	Path         string       `json:"path"`
	Config       PluginConfig `json:"config"`
	Loaded       bool         `json:"loaded"`
	Version      string       `json:"version,omitempty"`
	Description  string       `json:"description,omitempty"`
	Capabilities []string     `json:"capabilities,omitempty"`
}

// Manager manages the lifecycle of plugins.
type Manager struct {
	mu      sync.RWMutex
	plugins map[string]*Plugin
}

// NewManager creates a new plugin manager.
func NewManager() *Manager {
	return &Manager{
		plugins: make(map[string]*Plugin),
	}
}

// Load reads a plugin manifest from the plugin directory and registers it.
// It expects a plugin.json file at the root of the plugin path.
func (m *Manager) Load(config PluginConfig) (*Plugin, error) {
	if config.Type != "local" {
		return nil, fmt.Errorf("unsupported plugin type: %s", config.Type)
	}

	manifestPath := filepath.Join(config.Path, "plugin.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read plugin manifest at %s: %w", manifestPath, err)
	}

	var manifest pluginManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("failed to parse plugin manifest: %w", err)
	}

	if manifest.Name == "" {
		return nil, fmt.Errorf("plugin manifest missing required field: name")
	}

	plugin := &Plugin{
		Name:         manifest.Name,
		Path:         config.Path,
		Config:       config,
		Loaded:       true,
		Version:      manifest.Version,
		Description:  manifest.Description,
		Capabilities: manifest.Capabilities,
	}

	m.mu.Lock()
	m.plugins[plugin.Name] = plugin
	m.mu.Unlock()

	return plugin, nil
}

// Unload removes a plugin by name.
func (m *Manager) Unload(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.plugins[name]; !ok {
		return fmt.Errorf("plugin %q not found", name)
	}

	delete(m.plugins, name)
	return nil
}

// List returns all loaded plugins.
func (m *Manager) List() []*Plugin {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]*Plugin, 0, len(m.plugins))
	for _, p := range m.plugins {
		result = append(result, p)
	}
	return result
}

// Get returns a plugin by name, or nil if not found.
func (m *Manager) Get(name string) *Plugin {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.plugins[name]
}
