package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeTools answers yt-dlp by its last argument, and pages by URL.
func fakeResolver(yt map[string]string, pages map[string]string) *Resolver {
	return &Resolver{
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if out, ok := yt[args[len(args)-1]]; ok {
				return []byte(out), nil
			}
			return nil, errors.New("Unsupported URL")
		},
		fetch: func(_ context.Context, u string) (string, error) {
			if p, ok := pages[u]; ok {
				return p, nil
			}
			return "", errors.New("404 Not Found")
		},
	}
}

func TestResolve(t *testing.T) {
	r := fakeResolver(map[string]string{
		"https://www.youtube.com/watch?v=abc":             `{"title":"A Song","duration":214.5,"webpage_url":"https://www.youtube.com/watch?v=abc"}`,
		"https://soundcloud.com/x/sets/y":                 `{"_type":"playlist","entries":[{"title":"One","url":"https://soundcloud.com/x/one","duration":60},{"title":"Two","url":"https://soundcloud.com/x/two"}]}`,
		"ytsearch1:lofi beats":                            `{"_type":"playlist","entries":[{"title":"Lofi","url":"https://www.youtube.com/watch?v=lofi","duration":3600}]}`,
		"ytsearch1:Rick Astley - Never Gonna Give You Up": `{"_type":"playlist","entries":[{"title":"Rick (Official)","url":"https://www.youtube.com/watch?v=dQw4w9WgXcQ","duration":213}]}`,
	}, map[string]string{
		"https://open.spotify.com/track/4PTG3Z6ehGkBFwjybzWkR8": `<meta property="og:title" content="Never Gonna Give You Up"/><meta property="og:description" content="Rick Astley · Whenever You Need Somebody · Song · 1987"/><meta name="music:duration" content="214"/><meta name="music:musician_description" content="Rick Astley"/>`,
		"https://open.spotify.com/track/two":                    `<meta property="og:title" content="Together Forever"/><meta property="og:description" content="Rick Astley · Hold Me in Your Arms · Song · 1988"/>`,
		"https://open.spotify.com/album/al":                     `<meta property="og:title" content="Listen to an album"/><meta name="music:song" content="https://open.spotify.com/track/4PTG3Z6ehGkBFwjybzWkR8"/><meta name="music:song" content="https://open.spotify.com/track/two"/>`,
	})
	ctx := context.Background()

	ts, err := r.Resolve(ctx, "<https://www.youtube.com/watch?v=abc>")
	if err != nil || len(ts) != 1 || ts[0].Title != "A Song" || ts[0].Duration != 214500*time.Millisecond {
		t.Fatalf("youtube: %+v %v", ts, err)
	}
	ts, err = r.Resolve(ctx, "https://soundcloud.com/x/sets/y")
	if err != nil || len(ts) != 2 || ts[1].URL != "https://soundcloud.com/x/two" {
		t.Fatalf("set: %+v %v", ts, err)
	}
	ts, err = r.Resolve(ctx, "lofi beats")
	if err != nil || len(ts) != 1 || ts[0].URL != "https://www.youtube.com/watch?v=lofi" {
		t.Fatalf("search: %+v %v", ts, err)
	}

	// Spotify: what the page says, played from YouTube when its turn comes.
	for _, link := range []string{"https://open.spotify.com/intl-de/track/4PTG3Z6ehGkBFwjybzWkR8?si=x", "spotify:track:4PTG3Z6ehGkBFwjybzWkR8"} {
		ts, err = r.Resolve(ctx, link)
		if err != nil || len(ts) != 1 || ts[0].Title != "Rick Astley - Never Gonna Give You Up" || ts[0].URL != "" || ts[0].Duration != 214*time.Second {
			t.Fatalf("spotify %s: %+v %v", link, ts, err)
		}
	}
	if err := r.ensure(ctx, &ts[0]); err != nil || ts[0].URL != "https://www.youtube.com/watch?v=dQw4w9WgXcQ" {
		t.Fatalf("ensure: %+v %v", ts[0], err)
	}
	ts, err = r.Resolve(ctx, "https://open.spotify.com/album/al")
	if err != nil || len(ts) != 2 || ts[1].Title != "Rick Astley - Together Forever" {
		t.Fatalf("album: %+v %v", ts, err)
	}

	// Not something yt-dlp knows: a stream ffmpeg reads directly.
	ts, err = r.Resolve(ctx, "https://radio.example/live.mp3")
	if err != nil || len(ts) != 1 || !ts[0].Direct || ts[0].Title != "live.mp3" {
		t.Fatalf("direct: %+v %v", ts, err)
	}
	if _, err := r.Resolve(ctx, "   "); err == nil {
		t.Fatal("played nothing")
	}
	if _, err := r.Resolve(ctx, "https://open.spotify.com/playlist/gone"); err == nil || !strings.Contains(err.Error(), "Spotify playlist") {
		t.Fatalf("missing playlist: %v", err)
	}
}
