package cmd

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"

	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/frame"
	"github.com/mewkiz/flac/meta"
)

// writeFLAC losslessly encodes 16-bit mono PCM as a FLAC file.
func writeFLAC(path string, pcm []int16, sampleRate uint32) error {
	return writeFLACChannels(path, [][]int16{pcm}, sampleRate)
}

// writeFLACChannels losslessly encodes N channels of 16-bit PCM (channels must
// be 1 mono or 2 stereo, each slice the same length). Prefers ffmpeg's FLAC
// encoder — it produces standard, universally-playable files; our hand-rolled
// mewkiz frames tripped some players (macOS showed short files as 00:00 and
// mis-seeked longer ones, replaying audio). Falls back to the pure-Go mewkiz
// encoder when ffmpeg is unavailable.
func writeFLACChannels(path string, channels [][]int16, sampleRate uint32) error {
	if len(channels) == 0 || len(channels[0]) == 0 {
		return fmt.Errorf("no PCM samples")
	}
	nch := len(channels)
	if nch != 1 && nch != 2 {
		return fmt.Errorf("unsupported channel count %d", nch)
	}
	n := len(channels[0])
	for _, ch := range channels {
		if len(ch) != n {
			return fmt.Errorf("channel length mismatch")
		}
	}

	if err := writeFLACViaFFmpeg(path, channels, sampleRate); err == nil {
		return nil
	}
	return writeFLACViaMewkiz(path, channels, sampleRate)
}

// writeFLACViaFFmpeg pipes interleaved 16-bit PCM to ffmpeg and encodes a
// standard FLAC. Returns an error (triggering the mewkiz fallback) when ffmpeg
// is not on PATH or the encode fails.
func writeFLACViaFFmpeg(path string, channels [][]int16, sampleRate uint32) error {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return err
	}
	nch := len(channels)
	n := len(channels[0])
	raw := make([]byte, n*nch*2)
	for i := 0; i < n; i++ {
		for c := 0; c < nch; c++ {
			s := uint16(channels[c][i])
			off := (i*nch + c) * 2
			raw[off] = byte(s)
			raw[off+1] = byte(s >> 8)
		}
	}
	cmd := exec.Command(ffmpeg, "-y",
		"-f", "s16le", "-ar", strconv.Itoa(int(sampleRate)), "-ac", strconv.Itoa(nch),
		"-i", "pipe:0", "-c:a", "flac", "-compression_level", "8", path)
	cmd.Stdin = bytes.NewReader(raw)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

// writeFLACViaMewkiz is the pure-Go fallback encoder (no external tools).
func writeFLACViaMewkiz(path string, channels [][]int16, sampleRate uint32) error {
	nch := len(channels)
	n := len(channels[0])
	chLayout := frame.ChannelsMono
	if nch == 2 {
		chLayout = frame.ChannelsLR
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	const blockSize = 4096
	info := &meta.StreamInfo{
		BlockSizeMin:  blockSize,
		BlockSizeMax:  blockSize,
		SampleRate:    sampleRate,
		NChannels:     uint8(nch),
		BitsPerSample: 16,
		NSamples:      uint64(n),
	}
	enc, err := flac.NewEncoder(f, info)
	if err != nil {
		return err
	}
	defer enc.Close()

	for off := 0; off < n; off += blockSize {
		end := off + blockSize
		if end > n {
			end = n
		}
		bn := end - off
		subframes := make([]*frame.Subframe, nch)
		for c := 0; c < nch; c++ {
			samples := make([]int32, bn)
			for i, s := range channels[c][off:end] {
				samples[i] = int32(s)
			}
			subframes[c] = &frame.Subframe{
				SubHeader: frame.SubHeader{Pred: frame.PredVerbatim},
				NSamples:  bn,
				Samples:   samples,
			}
		}
		fr := &frame.Frame{
			Header: frame.Header{
				HasFixedBlockSize: true,
				BlockSize:         uint16(bn),
				SampleRate:        0,
				Channels:          chLayout,
				BitsPerSample:     16,
			},
			Subframes: subframes,
		}
		if err := enc.WriteFrame(fr); err != nil {
			return err
		}
	}
	return nil
}
