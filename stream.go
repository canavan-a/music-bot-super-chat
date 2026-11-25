package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"

	"github.com/canavan-a/super-chat/shared/bot/audio"
)

// Streamer plays a track: yt-dlp fetches it, ffmpeg turns it into what the
// app asks voice peers for (Opus, mono, 32 kbps constant bitrate, 20 ms
// frames) in an Ogg stream, and the SDK sends that on as it arrives.
type Streamer struct {
	YTDLP  string // default "yt-dlp"
	FFmpeg string // default "ffmpeg"
}

// opusArgs is the encode the app's call/sdp.ts asks for.
var opusArgs = []string{"-vn", "-c:a", "libopus", "-b:a", "32k", "-vbr", "off", "-ac", "1", "-ar", "48000", "-frame_duration", "20", "-application", "audio", "-f", "ogg", "pipe:1"}

// stream is a running track: its source, and what stops it.
type stream struct {
	audio.Source
	cancel context.CancelFunc
	wait   func() error
	stderr *tail
}

// Open starts a track playing into an audio.Source. Close stops it.
func (s *Streamer) Open(ctx context.Context, t Track) (*stream, error) {
	ctx, cancel := context.WithCancel(ctx)
	errs := &tail{}
	var procs []*exec.Cmd
	var ff *exec.Cmd
	if t.Direct {
		// Reconnect a stream that drops (internet radio).
		args := append([]string{"-hide_banner", "-loglevel", "error", "-reconnect", "1", "-reconnect_streamed", "1", "-reconnect_delay_max", "5", "-i", t.URL}, opusArgs...)
		ff = exec.CommandContext(ctx, s.ffmpeg(), args...)
	} else {
		dl := exec.CommandContext(ctx, s.ytdlp(), "-f", "bestaudio/best", "-o", "-", "--no-playlist", "--quiet", "--no-warnings", "--no-part", t.URL)
		dl.Stderr = errs
		args := append([]string{"-hide_banner", "-loglevel", "error", "-i", "pipe:0"}, opusArgs...)
		ff = exec.CommandContext(ctx, s.ffmpeg(), args...)
		pipe, err := dl.StdoutPipe()
		if err != nil {
			cancel()
			return nil, err
		}
		ff.Stdin = pipe
		procs = append(procs, dl)
	}
	ff.Stderr = errs
	out, err := ff.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	procs = append(procs, ff)
	for i, p := range procs {
		if err := p.Start(); err != nil {
			cancel()
			for _, q := range procs[:i] {
				_ = q.Wait()
			}
			if errors.Is(err, exec.ErrNotFound) {
				return nil, fmt.Errorf("%s isn't installed", p.Path)
			}
			return nil, err
		}
	}
	wait := func() error {
		var first error
		for _, p := range procs {
			if err := p.Wait(); err != nil && first == nil {
				first = err
			}
		}
		return first
	}
	// Reads the Ogg headers: the first sound (or failure) from the pipeline.
	src, err := audio.Ogg(out)
	if err != nil {
		cancel()
		_ = wait()
		return nil, fmt.Errorf("couldn't play %s: %s", t.Title, errs.reason(err))
	}
	return &stream{Source: src, cancel: cancel, wait: wait, stderr: errs}, nil
}

// Close stops the track's processes.
func (s *stream) Close() {
	s.cancel()
	_ = s.wait()
}

func (s *Streamer) ytdlp() string {
	if s.YTDLP != "" {
		return s.YTDLP
	}
	return "yt-dlp"
}

func (s *Streamer) ffmpeg() string {
	if s.FFmpeg != "" {
		return s.FFmpeg
	}
	return "ffmpeg"
}

// tail keeps the end of a process's stderr, to say why a track failed.
type tail struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 4096 {
		t.buf = t.buf[len(t.buf)-4096:]
	}
	return len(p), nil
}

// reason is the last line the tools printed, or err.
func (t *tail) reason(err error) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(string(bytes.TrimSpace(t.buf))), "\n")
	if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
		return strings.TrimPrefix(last, "ERROR: ")
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return "it ended before any sound came"
	}
	return err.Error()
}

// Reason says why the track stopped early: the tools' last words, or err.
func (s *stream) Reason(err error) string { return s.stderr.reason(err) }
