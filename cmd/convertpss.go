package cmd

import (
	"encoding/binary"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// convertPSSFile handles a PS2 FMV (.PSS = PlayStation Stream, MPEG-PS:
// MPEG-2 video + PS2 SShd/SSbd 16-bit PCM audio in private-stream 1). It always
// copies the raw .PSS verbatim (the lossless disc original, mirroring the .vag
// audio pattern), and — if ffmpeg is on PATH — transcodes to a
// universally-playable H.264 .mp4, muxing in the extracted audio track.
// ffmpeg is optional: absent, the raw .PSS is still preserved.
func convertPSSFile(path, outDir string, verbose bool) {
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return
	}
	base := filepath.Base(path)
	rawOut := filepath.Join(outDir, base)
	if err := copyFile(path, rawOut); err != nil {
		logf("Error copying %s: %v\n", base, err)
		return
	}

	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		if verbose {
			logf("  → %s (ffmpeg not found; install it to also get an .mp4)\n", rawOut)
		}
		return
	}

	stem := strings.TrimSuffix(base, filepath.Ext(base))
	mp4Out := filepath.Join(outDir, stem+".mp4")

	// Extract the PS2 SShd/SSbd PCM audio (ffmpeg can't demux the private
	// stream). If present, write a temp WAV and mux it with the video.
	pss, _ := os.ReadFile(path)
	wavPath := ""
	if wav, ok := extractPSSAudioWAV(pss); ok {
		wavPath = filepath.Join(outDir, stem+".pss_audio.wav")
		if os.WriteFile(wavPath, wav, 0644) != nil {
			wavPath = ""
		}
	}

	var cmd *exec.Cmd
	if wavPath != "" {
		cmd = exec.Command(ffmpeg, "-y", "-i", path, "-i", wavPath,
			"-map", "0:v:0", "-map", "1:a:0",
			"-c:v", "libx264", "-crf", "18", "-preset", "medium", "-pix_fmt", "yuv420p",
			"-c:a", "aac", "-b:a", "192k", "-shortest", "-movflags", "+faststart", mp4Out)
	} else {
		cmd = exec.Command(ffmpeg, "-y", "-i", path,
			"-c:v", "libx264", "-crf", "18", "-preset", "medium", "-pix_fmt", "yuv420p",
			"-movflags", "+faststart", mp4Out)
	}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	runErr := cmd.Run()
	if wavPath != "" {
		os.Remove(wavPath) // temp; the audio lives in the mp4
	}
	if runErr != nil {
		if verbose {
			logf("  → %s (ffmpeg transcode failed: %v)\n", rawOut, runErr)
		}
		return
	}
	if verbose {
		logf("  → %s + %s\n", rawOut, mp4Out)
	}
}

// extractPSSAudioWAV pulls the PS2 SShd/SSbd audio out of an MPEG-PS .PSS and
// returns it as a 16-bit PCM WAV. The audio lives in private-stream 1 (PES id
// 0xBD): each packet payload carries a 4-byte sub-stream prefix; the first
// payload begins with an "SShd" header (sampleRate/channels/interleave) then an
// "SSbd" body marker, after which the (block-interleaved) 16-bit PCM begins.
// Returns ok=false for video-only PSS.
func extractPSSAudioWAV(d []byte) ([]byte, bool) {
	var body []byte
	sampleRate, channels, interleave := 0, 0, 0
	first := true
	for i := 0; i+6 < len(d); {
		if d[i] == 0 && d[i+1] == 0 && d[i+2] == 1 && d[i+3] == 0xBD {
			pesLen := int(d[i+4])<<8 | int(d[i+5])
			hdrDataLen := int(d[i+8])
			start := i + 9 + hdrDataLen
			end := i + 6 + pesLen
			if end > len(d) {
				end = len(d)
			}
			if start < end {
				c := d[start:end]
				if len(c) >= 4 {
					c = c[4:] // strip per-packet sub-stream prefix
				}
				if first {
					// Parse SShd (sampleRate@+12, channels@+16, interleave@+20),
					// then skip to after the SSbd size field.
					if len(c) >= 32 && string(c[0:4]) == "SShd" {
						sampleRate = int(binary.LittleEndian.Uint32(c[12:16]))
						channels = int(binary.LittleEndian.Uint32(c[16:20]))
						interleave = int(binary.LittleEndian.Uint32(c[20:24]))
						c = c[4+4+24:] // "SShd" + size + 24-byte header
						if len(c) >= 8 && string(c[0:4]) == "SSbd" {
							c = c[4+4:] // "SSbd" + size
						}
						first = false
					} else {
						return nil, false // not SShd audio
					}
				}
				body = append(body, c...)
			}
			i = end
		} else {
			i++
		}
	}
	if first || sampleRate == 0 || channels < 1 || interleave <= 0 {
		return nil, false
	}

	// De-interleave `interleave`-byte blocks round-robin across channels, then
	// pack as sample-interleaved 16-bit PCM.
	chans := make([][]byte, channels)
	for bi := 0; (bi+1)*interleave <= len(body); bi++ {
		ch := bi % channels
		chans[ch] = append(chans[ch], body[bi*interleave:(bi+1)*interleave]...)
	}
	frames := len(chans[0]) / 2
	for _, cb := range chans {
		if cb2 := len(cb) / 2; cb2 < frames {
			frames = cb2
		}
	}
	pcm := make([]byte, frames*channels*2)
	for f := 0; f < frames; f++ {
		for ch := 0; ch < channels; ch++ {
			copy(pcm[(f*channels+ch)*2:], chans[ch][f*2:f*2+2])
		}
	}
	return buildWAV(pcm, sampleRate, channels), true
}

// buildWAV wraps 16-bit little-endian PCM in a canonical 44-byte WAV header.
func buildWAV(pcm []byte, sampleRate, channels int) []byte {
	byteRate := sampleRate * channels * 2
	blockAlign := channels * 2
	var h [44]byte
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+len(pcm)))
	copy(h[8:], "WAVE")
	copy(h[12:], "fmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1) // PCM
	binary.LittleEndian.PutUint16(h[22:], uint16(channels))
	binary.LittleEndian.PutUint32(h[24:], uint32(sampleRate))
	binary.LittleEndian.PutUint32(h[28:], uint32(byteRate))
	binary.LittleEndian.PutUint16(h[32:], uint16(blockAlign))
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(len(pcm)))
	return append(h[:], pcm...)
}

// copyFile copies src to dst verbatim.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
