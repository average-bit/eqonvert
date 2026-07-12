package cmd

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// externalTools are external programs the converter shells out to. ffmpeg
// encodes/decodes audio and video; openmpt123 (libopenmpt) renders tracker
// modules. Both are optional — see warnMissingTools.
var externalTools = []struct{ name, brew, why, verArg string }{
	{"ffmpeg", "ffmpeg", "audio (FLAC) and video (FMV) encoding", "-version"},
	{"openmpt123", "libopenmpt", "rendering .xm tracker music to FLAC", "--version"},
}

// externalToolsReport lists the external programs eqonvert shells out to and
// the version detected on PATH (or an install hint if missing). Surfaced by
// `eqonvert --version`.
func externalToolsReport() string {
	var b strings.Builder
	b.WriteString("external tools:")
	for _, t := range externalTools {
		path, err := exec.LookPath(t.name)
		if err != nil {
			b.WriteString(fmt.Sprintf("\n  %-11s not found  (brew install %s)", t.name, t.brew))
			continue
		}
		b.WriteString(fmt.Sprintf("\n  %-11s %s", t.name, detectVersion(path, t.verArg)))
	}
	return b.String()
}

// detectVersion runs `tool <verArg>` and returns its first non-empty output
// line (trimmed). Best-effort — never fails.
func detectVersion(path, verArg string) string {
	out, _ := exec.Command(path, verArg).CombinedOutput()
	for _, line := range strings.Split(string(out), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			if len(s) > 68 {
				s = s[:68]
				if i := strings.LastIndexByte(s, ' '); i > 40 {
					s = s[:i]
				}
				s += " …"
			}
			return s
		}
	}
	return "installed (version unknown)"
}

// warnMissingTools logs a non-fatal notice for any external tool not on PATH.
// The tools are optional enhancers, not hard requirements: models convert
// without them, audio falls back to a pure-Go FLAC encoder, and video/tracker
// output is simply skipped. This lets a model-only job run on a bare system
// while telling the user why audio/video may be missing.
func warnMissingTools() {
	for _, t := range externalTools {
		if _, err := exec.LookPath(t.name); err != nil {
			logf("note: %s not found — %s will be limited or skipped (brew install %s)\n", t.name, t.why, t.brew)
		}
	}
}

// renderXMToFLAC renders a tracker module (.xm) to a sibling .flac using
// openmpt123 (module → WAV) then ffmpeg (WAV → FLAC), keeping the .xm source.
// Best-effort: returns an error the caller may ignore/log.
func renderXMToFLAC(xmPath string) error {
	openmpt, err := exec.LookPath("openmpt123")
	if err != nil {
		return err
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return err
	}
	// openmpt123 --render writes "<xmPath>.wav".
	render := exec.Command(openmpt, "--render", xmPath)
	render.Stdout = io.Discard
	render.Stderr = io.Discard
	if err := render.Run(); err != nil {
		return err
	}
	wavPath := xmPath + ".wav"
	defer os.Remove(wavPath)

	flacPath := strings.TrimSuffix(xmPath, filepath.Ext(xmPath)) + ".flac"
	enc := exec.Command(ffmpeg, "-y", "-i", wavPath,
		"-c:a", "flac", "-compression_level", "8", "-sample_fmt", "s16", flacPath)
	enc.Stdout = io.Discard
	enc.Stderr = io.Discard
	return enc.Run()
}
