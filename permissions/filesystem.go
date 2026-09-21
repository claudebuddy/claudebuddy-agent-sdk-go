package permissions

import (
	"os"
	"path/filepath"
	"strings"
)

// WorkingDirectory represents an allowed working directory.
type WorkingDirectory struct {
	Path     string `json:"path"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

// FilesystemValidator validates file operations against allowed paths.
type FilesystemValidator struct {
	PrimaryDir     string
	AdditionalDirs []WorkingDirectory
}

// NewFilesystemValidator creates a new filesystem validator.
func NewFilesystemValidator(primaryDir string, additionalDirs []WorkingDirectory) *FilesystemValidator {
	return &FilesystemValidator{
		PrimaryDir:     primaryDir,
		AdditionalDirs: additionalDirs,
	}
}

// ValidatePath checks if a path is within allowed directories.
// Returns (allowed, readOnly, error).
func (v *FilesystemValidator) ValidatePath(path string) (bool, bool, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false, false, err
	}

	// Resolve symlinks
	realPath, err := filepath.EvalSymlinks(absPath)
	if err != nil && !os.IsNotExist(err) {
		// For new files, resolve parent
		parentPath := filepath.Dir(absPath)
		realParent, err2 := filepath.EvalSymlinks(parentPath)
		if err2 != nil {
			realPath = absPath
		} else {
			realPath = filepath.Join(realParent, filepath.Base(absPath))
		}
	} else if err != nil {
		realPath = absPath
	}

	// Check primary directory
	if v.PrimaryDir != "" {
		absPrimary, _ := filepath.Abs(v.PrimaryDir)
		if isSubpath(realPath, absPrimary) {
			return true, false, nil
		}
	}

	// Check additional directories
	for _, dir := range v.AdditionalDirs {
		absDir, _ := filepath.Abs(dir.Path)
		if isSubpath(realPath, absDir) {
			return true, dir.ReadOnly, nil
		}
	}

	// Allow temp directories
	tmpDir := os.TempDir()
	if isSubpath(realPath, tmpDir) {
		return true, false, nil
	}

	// Allow home directory config files
	if home, err := os.UserHomeDir(); err == nil {
		claudebuddyDir := filepath.Join(home, ".claudebuddy")
		if isSubpath(realPath, claudebuddyDir) {
			return true, false, nil
		}
	}

	return false, false, nil
}

// ValidateWrite checks if writing to a path is allowed.
func (v *FilesystemValidator) ValidateWrite(path string) error {
	allowed, readOnly, err := v.ValidatePath(path)
	if err != nil {
		return err
	}
	if !allowed {
		// In SDK mode, we don't restrict by default
		return nil
	}
	if readOnly {
		return &PathError{Path: path, Reason: "directory is read-only"}
	}
	return nil
}

// PathError represents a path validation error.
type PathError struct {
	Path   string
	Reason string
}

func (e *PathError) Error() string {
	return e.Reason + ": " + e.Path
}

// isSubpath checks if child is under parent.
func isSubpath(child, parent string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return !strings.HasPrefix(rel, "..") && rel != ".."
}

// IsSensitivePath checks if a path is potentially sensitive.
func IsSensitivePath(path string) bool {
	base := filepath.Base(path)
	sensitiveNames := []string{
		".env", ".env.local", ".env.production",
		"credentials.json", "credentials.yaml",
		"secrets.json", "secrets.yaml",
		".aws/credentials", ".ssh/id_rsa", ".ssh/id_ed25519",
		"service-account.json",
	}
	for _, name := range sensitiveNames {
		if base == name || strings.HasSuffix(path, "/"+name) {
			return true
		}
	}

	sensitivePatterns := []string{
		".pem", ".key", ".p12", ".pfx",
	}
	ext := strings.ToLower(filepath.Ext(path))
	for _, pat := range sensitivePatterns {
		if ext == pat {
			return true
		}
	}

	return false
}
