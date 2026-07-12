package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "eqonvert",
	Short: "eqonvert — EQOA asset converter",
	Long: `eqonvert converts EQOA (PlayStation 2) game assets — character models,
zones, textures, and audio — into open formats (glTF, FLAC).

Point it at a disc image or an extracted game directory:

  eqonvert convert <ISO-or-directory> -o <output-directory>

Audio/video output uses ffmpeg + openmpt123 when present (optional — models
convert without them). Run 'eqonvert --version' to see detected tool versions.`,
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Help()
	},
}

// advancedCommands are the research/dev subcommands. They still work when
// invoked directly, but are hidden from --help so end users see just `convert`.
var advancedCommands = []string{
	"convert-zone", "decompress", "dump-body", "extract",
	"index", "inspect", "scene",
}

// verbose/quiet are global output-verbosity flags honored by logf / vlogf.
var (
	verbose bool
	quiet   bool
)

func init() {
	rootCmd.PersistentFlags().BoolVar(&verbose, "verbose", false, "print per-item detail")
	rootCmd.PersistentFlags().BoolVarP(&quiet, "quiet", "q", false, "suppress progress and status output")
}

// Execute runs the root command. version is stamped in at build time and
// surfaced via the `--version` flag / `eqonvert version`.
func Execute(version string) {
	rootCmd.Version = version
	rootCmd.SilenceUsage = true // a runtime error shouldn't dump the full usage
	// `--version` also reports the external tools and their detected versions.
	// Only run the (subprocess-based) detection when --version is actually asked.
	if versionRequested() {
		rootCmd.SetVersionTemplate("eqonvert {{.Version}}\n\n" + externalToolsReport() + "\n")
	}
	// Keep the help surface minimal: hide the shell-completion command and the
	// advanced/research subcommands (they remain fully functional if called).
	rootCmd.CompletionOptions.HiddenDefaultCmd = true
	for _, c := range rootCmd.Commands() {
		for _, name := range advancedCommands {
			if c.Name() == name {
				c.Hidden = true
			}
		}
	}
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1) // cobra has already printed the error to stderr
	}
}

// versionRequested reports whether the user asked for --version / -v, so the
// external-tool version detection only shells out when actually needed.
func versionRequested() bool {
	for _, a := range os.Args[1:] {
		if a == "--version" || a == "-v" {
			return true
		}
	}
	return false
}
