package cmd

import (
	"fmt"
	"os"
	"sync/atomic"
	"time"

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

// startHeartbeat re-renders bar once per second so its elapsed/ETA timer keeps
// advancing (and spinners keep spinning) during a long single step that fires no
// Add() — e.g. converting one huge TUNARIA.ESF, where the top-level bar sits on a
// single item for minutes and would otherwise look frozen. RenderBlank is
// mutex-guarded inside progressbar, so it is safe to call concurrently with the
// main loop's Add()/Describe(). The returned func stops the heartbeat and must be
// called when the bar finishes (wired through OptionOnCompletion).
func startHeartbeat(bar *progressbar.ProgressBar) func() {
	var done atomic.Bool
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for range t.C {
			if done.Load() {
				return
			}
			_ = bar.RenderBlank()
		}
	}()
	return func() { done.Store(true) }
}

// newBar returns a determinate progress bar writing to stderr.
func newBar(total int, desc string) *progressbar.ProgressBar {
	stopHB := func() {}
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
		opts = append(opts, progressbar.OptionOnCompletion(func() {
			stopHB()
			fmt.Fprint(os.Stderr, "\n")
		}))
	}
	bar := progressbar.NewOptions(total, opts...)
	if progressVisible() {
		stopHB = startHeartbeat(bar)
	}
	return bar
}

// newSpinner returns an indeterminate spinner writing to stderr.
func newSpinner(desc string) *progressbar.ProgressBar {
	stopHB := func() {}
	opts := []progressbar.Option{
		progressbar.OptionSetWriter(os.Stderr),
		progressbar.OptionSpinnerType(14),
		progressbar.OptionSetDescription(desc),
		progressbar.OptionSetVisibility(progressVisible()),
		progressbar.OptionClearOnFinish(),
	}
	if progressVisible() {
		opts = append(opts, progressbar.OptionOnCompletion(func() {
			stopHB()
			fmt.Fprint(os.Stderr, "\n")
		}))
	}
	bar := progressbar.NewOptions(-1, opts...)
	if progressVisible() {
		stopHB = startHeartbeat(bar)
	}
	return bar
}
