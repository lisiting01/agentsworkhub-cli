//go:build !windows

package daemon

// resolveGitBashPath is a no-op on non-Windows platforms.
func resolveGitBashPath() string { return "" }

// normalizePath is a no-op on non-Windows platforms.
func normalizePath(p string) string { return p }
