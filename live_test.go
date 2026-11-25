//go:build live

// Real sites, real yt-dlp and ffmpeg: go test -tags live -run Live -v
package main

import (
	"context"
	"testing"
	"time"
)

func TestLiveStreams(t *testing.T) {
	r := &Resolver{run: run, fetch: fetch}
	s := &Streamer{}
	for _, in := range []string{
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://open.spotify.com/track/4PTG3Z6ehGkBFwjybzWkR8",
		"scsearch1:lofi hip hop",
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		var ts []Track
		var err error
		if in[:8] == "scsearch" { // SoundCloud's search, as a stand-in for a SoundCloud link
			var info *ytInfo
			if info, err = r.ytdlp(ctx, in); err == nil {
				ts = info.tracks("")
			}
		} else {
			ts, err = r.Resolve(ctx, in)
		}
		if err != nil || len(ts) == 0 {
			t.Fatalf("%s: %v", in, err)
		}
		tr := ts[0]
		if err := r.ensure(ctx, &tr); err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		st, err := s.Open(ctx, tr)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		var played time.Duration
		n := 0
		for played < 3*time.Second {
			_, d, err := st.Next()
			if err != nil {
				t.Fatalf("%s: after %s: %v (%s)", in, played, err, st.Reason(err))
			}
			played += d
			n++
		}
		st.Close()
		cancel()
		t.Logf("%s → %q: %d packets, %s", in, tr.Title, n, played)
	}
}
