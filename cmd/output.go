package cmd

import (
	"fmt"
	"os"
)

// logf writes a status / progress / diagnostic line to stderr, keeping stdout
// clean for machine-readable results (12-factor CLI: mind the streams).
// Suppressed when --quiet is set.
func logf(format string, a ...any) {
	if quiet {
		return
	}
	fmt.Fprintf(os.Stderr, format, a...)
}

// vlogf is like logf but only prints when --verbose is set.
func vlogf(format string, a ...any) {
	if verbose {
		logf(format, a...)
	}
}
