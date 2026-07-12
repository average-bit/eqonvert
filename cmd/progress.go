package cmd

import (
	"fmt"
	"os"

	"github.com/schollz/progressbar/v3"
	"golang.org/x/term"
)

// stderrTTY reports whether stderr is an interactive terminal. Progress bars
// and spinners only animate on a TTY; when output is piped or redirected (CI
// logs, `2> file`) they render nothing to avoid carriage-return spam — the
// summary lines from logf carry the outcome instead.
var stderrTTY = term.IsTerminal(int(os.Stderr.Fd()))

// progressVisible reports whether animated progress should be shown.
func progressVisible() bool { return stderrTTY && !quiet }

// newBar returns a determinate progress bar writing to stderr.
func newBar(total int, desc string) *progressbar.ProgressBar {
	opts := []progressbar.Option{
		progressbar.OptionSetWriter(os.Stderr),
		progressbar.OptionSetDescription(desc),
		progressbar.OptionShowCount(),
		progressbar.OptionSetWidth(35),
		progressbar.OptionSetVisibility(progressVisible()),
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        "█",
			SaucerPadding: "░",
			BarStart:      "[",
			BarEnd:        "]",
		}),
	}
	if progressVisible() {
		opts = append(opts, progressbar.OptionOnCompletion(func() { fmt.Fprint(os.Stderr, "\n") }))
	}
	return progressbar.NewOptions(total, opts...)
}

// newSpinner returns an indeterminate spinner writing to stderr.
func newSpinner(desc string) *progressbar.ProgressBar {
	opts := []progressbar.Option{
		progressbar.OptionSetWriter(os.Stderr),
		progressbar.OptionSpinnerType(14),
		progressbar.OptionSetDescription(desc),
		progressbar.OptionSetVisibility(progressVisible()),
		progressbar.OptionClearOnFinish(),
	}
	if progressVisible() {
		opts = append(opts, progressbar.OptionOnCompletion(func() { fmt.Fprint(os.Stderr, "\n") }))
	}
	return progressbar.NewOptions(-1, opts...)
}
