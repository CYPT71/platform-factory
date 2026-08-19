// Package shellquote renders a command and its arguments as a single,
// safely shell-quoted log line - for display only, never for actually
// invoking a shell (every real command execution in this repo uses
// os/exec directly with an argv slice, never a shell).
package shellquote

import "strings"

// Command renders name and args as one shell-quoted string: any
// argument that is empty or contains whitespace or shell metacharacters
// is single-quoted, with embedded single quotes escaped. Used to log a
// command a caller is about to run in a form that can be copy-pasted
// back into a shell unambiguously.
func Command(name string, args []string) string {
	values := append([]string{name}, args...)
	for index, value := range values {
		if value == "" || strings.ContainsAny(value, " \t\r\n'\"\\$") {
			values[index] = "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
		}
	}
	return strings.Join(values, " ")
}
