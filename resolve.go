package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
)

// Track is one song in a queue.
type Track struct {
	Title    string
	URL      string // what's played: a page yt-dlp understands, or (Direct) the audio itself
	Query    string // instead of URL until it's needed: a yt-dlp search (Spotify tracks)
	Duration time.Duration
	Direct   bool   // a plain audio file or stream: ffmpeg reads it as is
	By       string // who queued it
}

// maxEntries caps what one playlist, album or set adds to a queue.
const maxEntries = 100

// Resolver turns what someone typed after "play" into tracks.
type Resolver struct {
	// YTDLP is the yt-dlp binary (default "yt-dlp", from PATH).
	YTDLP string
	// run runs a command and returns its stdout (tests replace it).
	run func(ctx context.Context, name string, args ...string) ([]byte, error)
	// fetch gets a web page (Spotify's), for tests to replace.
	fetch func(ctx context.Context, url string) (string, error)
}

// Resolve accepts a link (YouTube, SoundCloud, Spotify, most sites yt-dlp
// knows, or a direct audio link or stream) or words to search YouTube for.
func (r *Resolver) Resolve(ctx context.Context, input string) ([]Track, error) {
	s := strings.Trim(strings.TrimSpace(input), "<>")
	if s == "" {
		return nil, errors.New("play what? A link, or words to search for")
	}
	if isSpotify(s) {
		return r.spotify(ctx, s)
	}
	if !looksLikeURL(s) {
		return r.search(ctx, s)
	}
	info, err := r.ytdlp(ctx, s)
	if err != nil {
		// Not something yt-dlp knows: a plain audio file or a radio stream,
		// which ffmpeg can read directly.
		u, perr := url.Parse(s)
		if perr != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return nil, err
		}
		name := path.Base(u.Path)
		if name == "" || name == "/" || name == "." {
			name = u.Host
		}
		return []Track{{Title: name, URL: s, Direct: true}}, nil
	}
	return info.tracks(s), nil
}

// search is YouTube's top result for some words.
func (r *Resolver) search(ctx context.Context, words string) ([]Track, error) {
	info, err := r.ytdlp(ctx, "ytsearch1:"+words)
	if err != nil {
		return nil, err
	}
	ts := info.tracks("")
	if len(ts) == 0 {
		return nil, fmt.Errorf("nothing found for %q", words)
	}
	return ts[:1], nil
}

// ensure resolves a track queued as a search (Spotify) into a real URL.
func (r *Resolver) ensure(ctx context.Context, t *Track) error {
	if t.URL != "" {
		return nil
	}
	info, err := r.ytdlp(ctx, t.Query)
	if err != nil {
		return err
	}
	ts := info.tracks("")
	if len(ts) == 0 {
		return fmt.Errorf("couldn't find %s on YouTube", t.Title)
	}
	t.URL = ts[0].URL
	if t.Duration == 0 {
		t.Duration = ts[0].Duration
	}
	return nil
}

// ---- yt-dlp ----

type ytInfo struct {
	Type       string   `json:"_type"`
	Title      string   `json:"title"`
	Duration   float64  `json:"duration"`
	WebpageURL string   `json:"webpage_url"`
	URL        string   `json:"url"`
	Entries    []ytInfo `json:"entries"`
}

func (r *Resolver) ytdlp(ctx context.Context, target string) (*ytInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	out, err := r.run(ctx, r.bin(), "-J", "--flat-playlist", "--no-warnings", "--playlist-end", fmt.Sprint(maxEntries), target)
	if err != nil {
		return nil, err
	}
	var info ytInfo
	if err := json.Unmarshal(out, &info); err != nil {
		return nil, fmt.Errorf("yt-dlp said something unexpected: %w", err)
	}
	return &info, nil
}

func (r *Resolver) bin() string {
	if r.YTDLP != "" {
		return r.YTDLP
	}
	return "yt-dlp"
}

// tracks is a single item, or a playlist's entries.
func (i *ytInfo) tracks(asked string) []Track {
	if i.Type == "playlist" {
		var out []Track
		for _, e := range i.Entries {
			if t, ok := e.track(""); ok {
				out = append(out, t)
			}
			if len(out) == maxEntries {
				break
			}
		}
		return out
	}
	if t, ok := i.track(asked); ok {
		return []Track{t}
	}
	return nil
}

func (i *ytInfo) track(asked string) (Track, bool) {
	u := i.WebpageURL
	if u == "" {
		u = i.URL
	}
	if u == "" {
		u = asked
	}
	if u == "" {
		return Track{}, false
	}
	title := i.Title
	if title == "" {
		title = u
	}
	return Track{Title: title, URL: u, Duration: time.Duration(i.Duration * float64(time.Second))}, true
}

func looksLikeURL(s string) bool {
	if strings.ContainsAny(s, " \t\n") {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https")
}

// ---- Spotify ----
//
// Spotify's audio is DRM-protected: nothing can download it. Its public pages
// do say what each track is, though, so a Spotify track plays as YouTube's
// best match for "artist - title" (as spotDL does). Albums and playlists
// list their tracks on the page too.

var (
	spotifyURL = regexp.MustCompile(`^https?://open\.spotify\.com/(?:intl-[a-z-]+/)?(track|album|playlist)/([A-Za-z0-9]+)`)
	spotifyURI = regexp.MustCompile(`^spotify:(track|album|playlist):([A-Za-z0-9]+)$`)
	metaTag    = regexp.MustCompile(`<meta\s+(?:property|name)="([^"]+)"\s+content="([^"]*)"`)
)

func isSpotify(s string) bool { return spotifyURL.MatchString(s) || spotifyURI.MatchString(s) }

func (r *Resolver) spotify(ctx context.Context, s string) ([]Track, error) {
	m := spotifyURL.FindStringSubmatch(s)
	if m == nil {
		m = spotifyURI.FindStringSubmatch(s)
	}
	kind, id := m[1], m[2]
	page := "https://open.spotify.com/" + kind + "/" + id
	if kind == "track" {
		t, err := r.spotifyTrack(ctx, page)
		if err != nil {
			return nil, err
		}
		return []Track{t}, nil
	}
	html, err := r.fetch(ctx, page)
	if err != nil {
		return nil, fmt.Errorf("couldn't read that Spotify %s: %w", kind, err)
	}
	var songs []string
	for _, mt := range metaTag.FindAllStringSubmatch(html, -1) {
		if mt[1] == "music:song" && len(songs) < maxEntries {
			songs = append(songs, unescape(mt[2]))
		}
	}
	if len(songs) == 0 {
		return nil, fmt.Errorf("that Spotify %s lists no tracks I can read", kind)
	}
	// A few at a time: one page each.
	tracks := make([]Track, len(songs))
	errs := make([]error, len(songs))
	sem := make(chan struct{}, 4)
	done := make(chan struct{})
	for i, u := range songs {
		go func() {
			sem <- struct{}{}
			tracks[i], errs[i] = r.spotifyTrack(ctx, u)
			<-sem
			done <- struct{}{}
		}()
	}
	for range songs {
		<-done
	}
	var out []Track
	for i, t := range tracks {
		if errs[i] == nil {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil, errs[0]
	}
	return out, nil
}

// spotifyTrack reads one track's page: its title, artist and length.
func (r *Resolver) spotifyTrack(ctx context.Context, page string) (Track, error) {
	html, err := r.fetch(ctx, page)
	if err != nil {
		return Track{}, fmt.Errorf("couldn't read that Spotify track: %w", err)
	}
	meta := map[string]string{}
	for _, mt := range metaTag.FindAllStringSubmatch(html, -1) {
		if _, seen := meta[mt[1]]; !seen {
			meta[mt[1]] = unescape(mt[2])
		}
	}
	title := meta["og:title"]
	if title == "" {
		return Track{}, errors.New("that Spotify track's page says nothing about it")
	}
	artist := meta["music:musician_description"]
	if artist == "" {
		// "Rick Astley · Whenever You Need Somebody · Song · 1987"
		artist, _, _ = strings.Cut(meta["og:description"], " · ")
	}
	name := title
	if artist != "" {
		name = artist + " - " + title
	}
	var dur time.Duration
	fmt.Sscan(meta["music:duration"], &dur)
	return Track{Title: name, Query: "ytsearch1:" + name, Duration: dur * time.Second}, nil
}

func unescape(s string) string {
	return strings.NewReplacer("&amp;", "&", "&quot;", `"`, "&#x27;", "'", "&#39;", "'", "&lt;", "<", "&gt;", ">").Replace(s)
}
