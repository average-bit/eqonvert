package cmd

import "github.com/spf13/cobra"

// devCmd groups the developer / reverse-engineering subcommands under a single
// `eqonvert dev ...` namespace so the top-level help stays focused on `convert`
// (the tool's purpose) while the RE tooling remains discoverable in one place.
//
// Declared as a package var (not inside init) so it exists before every other
// command file's init() runs and can be used as their AddCommand target
// regardless of file initialization order.
var devCmd = &cobra.Command{
	Use:   "dev",
	Short: "Developer & reverse-engineering tools (inspect, decompress, scene, …)",
	Long: `Developer and reverse-engineering utilities for EQOA asset files.

These inspect, decompress, and pull apart ESF/CSF data — useful when reversing
formats or debugging a conversion. For normal use, see 'eqonvert convert'.`,
}

func init() {
	rootCmd.AddCommand(devCmd)
}
