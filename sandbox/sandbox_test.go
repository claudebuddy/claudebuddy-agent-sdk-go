package sandbox

import (
	"testing"
)

func TestNewValidator(t *testing.T) {
	v := NewValidator(Settings{Enabled: true})
	if v == nil {
		t.Fatal("expected non-nil validator")
	}
	if !v.IsEnabled() {
		t.Error("expected enabled")
	}
}

func TestDisabledSandboxAllowsEverything(t *testing.T) {
	v := NewValidator(Settings{Enabled: false})

	if !v.IsCommandAllowed("rm -rf /") {
		t.Error("disabled sandbox should allow all commands")
	}
	if !v.IsFileAccessAllowed("/etc/passwd") {
		t.Error("disabled sandbox should allow all file access")
	}
	if !v.IsNetworkAllowed("evil.com") {
		t.Error("disabled sandbox should allow all network access")
	}
}

func TestCommandExclusion(t *testing.T) {
	v := NewValidator(Settings{
		Enabled:          true,
		ExcludedCommands: []string{"rm", "curl"},
	})

	if v.IsCommandAllowed("rm -rf /tmp") {
		t.Error("rm should be excluded")
	}
	if v.IsCommandAllowed("curl http://example.com") {
		t.Error("curl should be excluded")
	}
	if !v.IsCommandAllowed("ls -la") {
		t.Error("ls should be allowed")
	}
}

func TestCommandExclusionWithFullPath(t *testing.T) {
	v := NewValidator(Settings{
		Enabled:          true,
		ExcludedCommands: []string{"rm"},
	})

	if v.IsCommandAllowed("/bin/rm -rf /tmp") {
		t.Error("/bin/rm should be excluded (base name matches)")
	}
}

func TestAllowUnsandboxedCommands(t *testing.T) {
	v := NewValidator(Settings{
		Enabled:                  true,
		ExcludedCommands:         []string{"rm"},
		AllowUnsandboxedCommands: true,
	})

	if !v.IsCommandAllowed("rm -rf /") {
		t.Error("AllowUnsandboxedCommands should override exclusions")
	}
}

func TestEmptyCommandAllowed(t *testing.T) {
	v := NewValidator(Settings{Enabled: true, ExcludedCommands: []string{"rm"}})
	if !v.IsCommandAllowed("") {
		t.Error("empty command should be allowed")
	}
}

func TestFileAccessBlocked(t *testing.T) {
	v := NewValidator(Settings{
		Enabled: true,
		IgnoreViolations: &IgnoreViolations{
			FilePaths: []string{"/secrets"},
		},
	})

	if v.IsFileAccessAllowed("/secrets/key.pem") {
		t.Error("path under /secrets should be blocked")
	}
	if v.IsFileAccessAllowed("/secrets") {
		t.Error("exact path /secrets should be blocked")
	}
	if !v.IsFileAccessAllowed("/home/user/file.txt") {
		t.Error("/home/user/file.txt should be allowed")
	}
}

func TestFileAccessNoViolations(t *testing.T) {
	v := NewValidator(Settings{Enabled: true})
	if !v.IsFileAccessAllowed("/any/path") {
		t.Error("no IgnoreViolations should allow all paths")
	}
}

func TestNetworkBlocked(t *testing.T) {
	v := NewValidator(Settings{
		Enabled: true,
		IgnoreViolations: &IgnoreViolations{
			NetworkHosts: []string{"evil.com", "bad.org"},
		},
	})

	if v.IsNetworkAllowed("evil.com") {
		t.Error("evil.com should be blocked")
	}
	if v.IsNetworkAllowed("bad.org") {
		t.Error("bad.org should be blocked")
	}
	if !v.IsNetworkAllowed("good.com") {
		t.Error("good.com should be allowed")
	}
}

func TestNetworkNoViolations(t *testing.T) {
	v := NewValidator(Settings{Enabled: true})
	if !v.IsNetworkAllowed("any.host") {
		t.Error("no IgnoreViolations should allow all hosts")
	}
}
