//go:build windows

package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// resolveGitBashPath returns the path to git-bash on Windows for injection
// into CLAUDE_CODE_GIT_BASH_PATH before spawning Claude Code. Returns an
// empty string if the variable is already set or no known path exists.
func resolveGitBashPath() string {
	if os.Getenv("CLAUDE_CODE_GIT_BASH_PATH") != "" {
		return ""
	}

	candidates := []string{
		`C:\Program Files\Git\bin\bash.exe`,
		`C:\Program Files\AI-LaunchPad\Git\bin\bash.exe`,
		`C:\Program Files (x86)\Git\bin\bash.exe`,
	}

	if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
		candidates = append(candidates, filepath.Join(localAppData, `Programs\Git\bin\bash.exe`))
	}

	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// normalizePath converts MSYS/Cygwin-style Unix paths (e.g. /c/Users/foo) back
// to Windows native paths (C:\Users\foo). Git Bash automatically converts
// Windows paths passed on the command line, so a Go binary may receive
// /c/... when the user typed C:\.... Without this conversion exec.Command
// fails with "The directory name is invalid."
//
// Paths that do not match the MSYS pattern are returned unchanged.
func normalizePath(p string) string {
	if len(p) < 3 {
		return p
	}
	// Match /X/... where X is a single letter (drive letter).
	if p[0] == '/' && unicode.IsLetter(rune(p[1])) && p[2] == '/' {
		drive := strings.ToUpper(string(p[1]))
		rest := filepath.FromSlash(p[2:])
		return drive + ":" + rest
	}
	return p
}
