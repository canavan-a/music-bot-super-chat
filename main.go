// music-bot is a Super Chat music bot, built on the bot SDK in
// github.com/canavan-a/super-chat/shared/bot. Tag it in a chat:
//
//	@bot play <YouTube, SoundCloud or Spotify link, any link yt-dlp knows, or words to search>
//	@bot skip | stop | pause | resume | queue | exit
//
// It joins the chat's voice to play, keeps a queue per chat (in memory), and
// leaves voice after 5 minutes with nobody else in it, or with nothing
// playing or queued. Needs yt-dlp and ffmpeg (the flake bundles both).
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/canavan-a/super-chat/shared/bot"
	"github.com/canavan-a/super-chat/shared/bot/audio"
	"github.com/canavan-a/super-chat/shared/bot/cli"
	"github.com/canavan-a/super-chat/shared/bot/core"
)

func main() {
	cli.Main(func(b *bot.Bot) {
		core.Register(b)
		newMusic(b).register(b)
	})
}

// music is every chat's player.
type music struct {
	b       *bot.Bot
	res     *Resolver
	stream  *Streamer
	idle    time.Duration
	mu      sync.Mutex
	players map[string]*player
}

func newMusic(b *bot.Bot) *music {
	idle := 5 * time.Minute
	if d, err := time.ParseDuration(os.Getenv("MUSIC_BOT_IDLE")); err == nil && d > 0 {
		idle = d
	}
	ytdlp := os.Getenv("MUSIC_BOT_YTDLP")
	return &music{
		b:       b,
		res:     &Resolver{YTDLP: ytdlp, run: run, fetch: fetch},
		stream:  &Streamer{YTDLP: ytdlp, FFmpeg: os.Getenv("MUSIC_BOT_FFMPEG")},
		idle:    idle,
		players: map[string]*player{},
	}
}

func (m *music) player(room string) *player {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.players[room]
	if p == nil {
		p = &player{
			room: room, v: botVoice{m.b}, res: m.res, idle: m.idle, tick: 5 * time.Second,
			now:  time.Now,
			open: func(ctx context.Context, t Track) (source, error) { return m.stream.Open(ctx, t) },
		}
		m.players[room] = p
	}
	return p
}

func (m *music) register(b *bot.Bot) {
	b.Command("play", bot.Anyone, "play <link or words>: YouTube, SoundCloud, Spotify, most links, or a search", m.play)
	b.Command("skip", bot.Anyone, "skip: the next song", m.skip)
	b.Command("stop", bot.Anyone, "stop: stop the music and clear the queue", m.stop)
	b.Command("pause", bot.Anyone, "pause: pause the music", func(c *bot.Ctx) error { return m.pause(c, true) })
	b.Command("resume", bot.Anyone, "resume: carry on after pause", func(c *bot.Ctx) error { return m.pause(c, false) })
	b.Command("queue", bot.Anyone, "queue: what's playing and what's next", m.queue)
	b.Command("exit", bot.Anyone, "exit: stop the music and leave voice", m.exit)
	b.OnRemoved(func(_ context.Context, room string) {
		m.mu.Lock()
		delete(m.players, room)
		m.mu.Unlock()
	})
}

func (m *music) play(c *bot.Ctx) error {
	what := strings.Join(c.Args, " ")
	if what == "" {
		return c.Reply("Play what? Tag me with play and a link, or words to search for.")
	}
	tracks, err := m.res.Resolve(c, what)
	if err != nil {
		return c.Reply("Couldn't play that: " + err.Error())
	}
	for i := range tracks {
		tracks[i].By = c.Sender.Name
	}
	pos := m.player(c.Room.ID).add(c, tracks)
	switch {
	case len(tracks) > 1:
		return c.Reply(fmt.Sprintf("Added %d songs to the queue.", len(tracks)))
	case pos == 0:
		return c.Reply("Starting " + tracks[0].Title + "…")
	default:
		return c.Reply(fmt.Sprintf("Added %s to the queue (#%d).", describe(tracks[0]), pos))
	}
}

func (m *music) skip(c *bot.Ctx) error {
	t, ok := m.player(c.Room.ID).skip()
	if !ok {
		return c.Reply("Nothing's playing.")
	}
	return c.Reply("Skipped " + t.Title + ".")
}

func (m *music) exit(c *bot.Ctx) error {
	if !m.player(c.Room.ID).exit(c) {
		return c.Reply("I'm not in voice.")
	}
	return c.Reply("Bye! Left voice.")
}

func (m *music) stop(c *bot.Ctx) error {
	if !m.player(c.Room.ID).stop() {
		return c.Reply("Nothing's playing.")
	}
	return c.Reply("Stopped, and cleared the queue.")
}

func (m *music) pause(c *bot.Ctx, paused bool) error {
	if !m.player(c.Room.ID).setPaused(paused) {
		return c.Reply("Nothing's playing.")
	}
	if paused {
		return c.Reply("Paused. Tag me with resume to carry on.")
	}
	return c.Reply("Resumed.")
}

func (m *music) queue(c *bot.Ctx) error {
	cur, paused, next := m.player(c.Room.ID).list()
	if cur == nil && len(next) == 0 {
		return c.Reply("Nothing's playing, and the queue is empty.")
	}
	var b strings.Builder
	if cur != nil {
		state := "▶ Playing"
		if paused {
			state = "⏸ Paused"
		}
		fmt.Fprintf(&b, "%s: %s\n", state, describe(*cur))
	}
	for i, t := range next {
		if i == 10 {
			fmt.Fprintf(&b, "… and %d more\n", len(next)-10)
			break
		}
		fmt.Fprintf(&b, "%d. %s\n", i+1, describe(t))
	}
	return c.Reply(strings.TrimSpace(b.String()))
}

// ---- the SDK, behind the player's interfaces ----

type botVoice struct{ b *bot.Bot }

func (v botVoice) join(ctx context.Context, room string) (call, error) {
	c, err := v.b.Voice.Join(ctx, room)
	if err != nil {
		return nil, err
	}
	return botCall{c}, nil
}

func (v botVoice) others(room string) int { return len(v.b.Voice.Presence(room)) }

func (v botVoice) say(ctx context.Context, room, text string) {
	if err := v.b.Send(ctx, room, text); err != nil {
		slog.Warn("couldn't post", "room", room, "err", err)
	}
}

type botCall struct{ *bot.Call }

func (c botCall) Play(src audio.Source) handle { return c.Call.Play(src) }

// ---- running tools ----

func run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr tail
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%s took too long", name)
		}
		return nil, fmt.Errorf("%s", stderr.reason(err))
	}
	return out, nil
}

func fetch(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	// Spotify serves its pages' metadata to link previewers.
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; music-bot)")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("%s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return string(body), err
}
