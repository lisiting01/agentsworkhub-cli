package output

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/fatih/color"
)

var (
	Bold    = color.New(color.Bold).SprintFunc()
	Green   = color.New(color.FgGreen).SprintFunc()
	Yellow  = color.New(color.FgYellow).SprintFunc()
	Red     = color.New(color.FgRed).SprintFunc()
	Cyan    = color.New(color.FgCyan).SprintFunc()
	Faint   = color.New(color.Faint).SprintFunc()
)

// StatusColor returns a colored status string.
func StatusColor(status string) string {
	switch status {
	case "open":
		return Green(status)
	case "in_progress":
		return Cyan(status)
	case "submitted":
		return Yellow(status)
	case "revision":
		return Yellow(status)
	case "completed":
		return color.New(color.FgGreen, color.Bold).Sprint(status)
	case "cancelled":
		return Red(status)
	case "active":
		return Green(status)
	case "paused":
		return Yellow(status)
	case "idle":
		return Yellow(status)
	case "closed":
		return Red(status)
	default:
		return status
	}
}

// JSON prints any value as indented JSON.
func JSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// Table writes a table to stdout using tabwriter.
// headers and rows should align in column count.
func Table(headers []string, rows [][]string) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	headerCells := make([]string, len(headers))
	for i, h := range headers {
		headerCells[i] = Bold(strings.ToUpper(h))
	}
	fmt.Fprintln(w, strings.Join(headerCells, "\t"))
	for _, row := range rows {
		fmt.Fprintln(w, strings.Join(row, "\t"))
	}
	w.Flush()
}

// KeyValue prints a list of key-value pairs.
func KeyValue(pairs [][2]string) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, p := range pairs {
		fmt.Fprintf(w, "%s\t%s\n", Bold(p[0]+":"), p[1])
	}
	w.Flush()
}

// Success prints a green success message.
func Success(msg string) {
	fmt.Println(Green("✓ " + msg))
}

// Error prints a red error message to stderr.
func Error(msg string) {
	fmt.Fprintln(os.Stderr, Red("✗ "+msg))
}

// Warn prints a yellow warning message.
func Warn(msg string) {
	fmt.Println(Yellow("! " + msg))
}

// Truncate truncates a string to maxLen with ellipsis.
func Truncate(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-1] + "…"
}

// FormatTokens formats a large int as a readable number (e.g. 1,500,000).
func FormatTokens(n int64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	start := len(s) % 3
	if start > 0 {
		b.WriteString(s[:start])
	}
	for i := start; i < len(s); i += 3 {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}
