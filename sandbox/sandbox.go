package sandbox

import (
	"path/filepath"
	"strings"
)

// NetworkConfig controls network access within the sandbox.
type NetworkConfig struct {
	AllowUnixSockets    bool `json:"allowUnixSockets,omitempty"`
	AllowAllUnixSockets bool `json:"allowAllUnixSockets,omitempty"`
	AllowLocalBinding   bool `json:"allowLocalBinding,omitempty"`
	HTTPProxyPort       int  `json:"httpProxyPort,omitempty"`
	SocksProxyPort      int  `json:"socksProxyPort,omitempty"`
}

// IgnoreViolations specifies paths and hosts to ignore violations for.
type IgnoreViolations struct {
	FilePaths    []string `json:"filePaths,omitempty"`
	NetworkHosts []string `json:"networkHosts,omitempty"`
}

// Settings defines the sandbox configuration.
type Settings struct {
	Enabled                   bool              `json:"enabled"`
	AutoAllowBashIfSandboxed  bool              `json:"autoAllowBashIfSandboxed,omitempty"`
	ExcludedCommands          []string          `json:"excludedCommands,omitempty"`
	AllowUnsandboxedCommands  bool              `json:"allowUnsandboxedCommands,omitempty"`
	Network                   *NetworkConfig    `json:"network,omitempty"`
	IgnoreViolations          *IgnoreViolations `json:"ignoreViolations,omitempty"`
	EnableWeakerNestedSandbox bool              `json:"enableWeakerNestedSandbox,omitempty"`
}

// Validator checks if a tool operation is allowed under sandbox settings.
type Validator struct {
	settings Settings
}

// NewValidator creates a new sandbox validator with the given settings.
func NewValidator(settings Settings) *Validator {
	return &Validator{settings: settings}
}

// IsEnabled returns whether the sandbox is enabled.
func (v *Validator) IsEnabled() bool {
	return v.settings.Enabled
}

// IsCommandAllowed checks if a command is allowed to execute.
// When the sandbox is disabled, all commands are allowed.
// When enabled, commands in the ExcludedCommands list are blocked
// unless AllowUnsandboxedCommands is set.
func (v *Validator) IsCommandAllowed(command string) bool {
	if !v.settings.Enabled {
		return true
	}

	if v.settings.AllowUnsandboxedCommands {
		return true
	}

	// Extract the base command name (first word).
	base := strings.Fields(command)
	if len(base) == 0 {
		return true
	}
	cmdName := filepath.Base(base[0])

	for _, excluded := range v.settings.ExcludedCommands {
		if cmdName == excluded {
			return false
		}
	}

	return true
}

// IsFileAccessAllowed checks if file access to the given path is allowed.
// When the sandbox is disabled, all paths are allowed.
// When enabled, paths listed in IgnoreViolations.FilePaths are blocked.
func (v *Validator) IsFileAccessAllowed(path string) bool {
	if !v.settings.Enabled {
		return true
	}

	if v.settings.IgnoreViolations == nil {
		return true
	}

	cleanPath := filepath.Clean(path)
	for _, blocked := range v.settings.IgnoreViolations.FilePaths {
		blockedClean := filepath.Clean(blocked)
		if cleanPath == blockedClean || strings.HasPrefix(cleanPath, blockedClean+string(filepath.Separator)) {
			return false
		}
	}

	return true
}

// IsNetworkAllowed checks if network access to the given host is allowed.
// When the sandbox is disabled, all hosts are allowed.
// When enabled, hosts listed in IgnoreViolations.NetworkHosts are blocked.
func (v *Validator) IsNetworkAllowed(host string) bool {
	if !v.settings.Enabled {
		return true
	}

	if v.settings.IgnoreViolations == nil {
		return true
	}

	for _, blocked := range v.settings.IgnoreViolations.NetworkHosts {
		if host == blocked {
			return false
		}
	}

	return true
}
