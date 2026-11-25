package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/canavan-a/super-chat/shared/bot/audio"
)

// ---- fakes for the SDK side ----

type fakeVoice struct {
	mu      sync.Mutex
	people  int
	said    []string
	calls   []*fakeCall
	joinErr error
}

func (v *fakeVoice) join(context.Context, string) (call, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.joinErr != nil {
		return nil, v.joinErr
	}
	c := &fakeCall{done: make(chan struct{})}
	v.calls = append(v.calls, c)
	return c, nil
}
func (v *fakeVoice) others(string) int { v.mu.Lock(); defer v.mu.Unlock(); return v.people }
func (v *fakeVoice) say(_ context.Context, _ string, text string) {
	v.mu.Lock()
	v.said = append(v.said, text)
	v.mu.Unlock()
}
func (v *fakeVoice) saidAll() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return strings.Join(v.said, "\n")
}

type fakeCall struct {
	mu      sync.Mutex
	done    chan struct{}
	why     string
	playing []*fakeHandle
	once    sync.Once
}

func (c *fakeCall) Play(src audio.Source) handle {
	h := &fakeHandle{done: make(chan struct{}), src: src}
	c.mu.Lock()
	c.playing = append(c.playing, h)
	c.mu.Unlock()
	return h
}
func (c *fakeCall) end(why string)              { c.once.Do(func() { c.why = why; close(c.done) }) }
func (c *fakeCall) Leave(context.Context) error { c.end("left"); return nil }
func (c *fakeCall) Done() <-chan struct{}       { return c.done }
func (c *fakeCall) Why() string                 { <-c.done; return c.why }
func (c *fakeCall) current() *fakeHandle {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.playing[len(c.playing)-1]
}

type fakeHandle struct {
	done   chan struct{}
	once   sync.Once
	err    error
	paused bool
	src    audio.Source
}

func (h *fakeHandle) finish(err error)      { h.once.Do(func() { h.err = err; close(h.done) }) }
func (h *fakeHandle) Done() <-chan struct{} { return h.done }
func (h *fakeHandle) Err() error            { <-h.done; return h.err }
func (h *fakeHandle) Stop()                 { h.finish(nil) }
func (h *fakeHandle) Pause()                { h.paused = true }
func (h *fakeHandle) Resume()               { h.paused = false }
func (h *fakeHandle) Paused() bool          { return h.paused }

type fakeSource struct{ title string }

func (fakeSource) Next() ([]byte, time.Duration, error) { return nil, 0, io.EOF }
func (fakeSource) Close()                               {}
func (fakeSource) Reason(err error) string              { return err.Error() }

func newTestPlayer(v *fakeVoice, idle time.Duration) *player {
	return &player{
		room: "r", v: v, res: &Resolver{}, idle: idle, tick: 10 * time.Millisecond, now: time.Now,
		open: func(_ context.Context, t Track) (source, error) {
			if strings.HasPrefix(t.Title, "broken") {
				return nil, errors.New("couldn't play " + t.Title)
			}
			return fakeSource{t.Title}, nil
		},
	}
}

func waitFor(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !fn() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func tracks(titles ...string) []Track {
	var out []Track
	for _, s := range titles {
		out = append(out, Track{Title: s, URL: "https://x/" + s, By: "Alice"})
	}
	return out
}

func TestPlayerQueue(t *testing.T) {
	v := &fakeVoice{people: 1}
	p := newTestPlayer(v, time.Hour)
	ctx := context.Background()
	if pos := p.add(ctx, tracks("one")); pos != 0 {
		t.Fatalf("first song at #%d", pos)
	}
	waitFor(t, "one to play", func() bool { return strings.Contains(v.saidAll(), "Now playing: one, queued by Alice") })
	if pos := p.add(ctx, tracks("broken", "two")); pos != 1 {
		t.Fatalf("queued at #%d", pos)
	}
	cur, _, next := p.list()
	if cur.Title != "one" || len(next) != 2 {
		t.Fatalf("list: %+v %+v", cur, next)
	}

	// Paused and resumed.
	if !p.setPaused(true) {
		t.Fatal("couldn't pause")
	}
	if _, paused, _ := p.list(); !paused {
		t.Fatal("not paused")
	}
	p.setPaused(false)

	// one ends; broken can't play (said so), two plays.
	c := v.calls[0]
	c.current().finish(nil)
	waitFor(t, "two to play", func() bool { return strings.Contains(v.saidAll(), "Now playing: two") })
	if !strings.Contains(v.saidAll(), "couldn't play broken") {
		t.Fatalf("said:\n%s", v.saidAll())
	}
	// skip
	if s, ok := p.skip(); !ok || s.Title != "two" {
		t.Fatalf("skip: %+v %v", s, ok)
	}
	waitFor(t, "nothing playing", func() bool { cur, _, _ := p.list(); return cur == nil })
	if _, ok := p.skip(); ok {
		t.Fatal("skipped nothing")
	}
	// stop clears the queue too.
	p.add(ctx, tracks("three", "four"))
	waitFor(t, "three", func() bool { return strings.Contains(v.saidAll(), "Now playing: three") })
	if !p.stop() {
		t.Fatal("stop had nothing")
	}
	waitFor(t, "stopped", func() bool { cur, _, next := p.list(); return cur == nil && len(next) == 0 })
	if len(v.calls) != 1 {
		t.Fatalf("joined voice %d times", len(v.calls))
	}
}

func TestPlayerLeavesWhenAlone(t *testing.T) {
	v := &fakeVoice{people: 0}
	p := newTestPlayer(v, 50*time.Millisecond)
	p.add(context.Background(), tracks("one"))
	waitFor(t, "it to leave", func() bool { return strings.Contains(v.saidAll(), "Left voice: nobody was listening") })
	if v.calls[0].Why() != "left" {
		t.Fatal("didn't leave the call")
	}
	if cur, _, next := p.list(); cur != nil || len(next) != 0 {
		t.Fatal("still has music after leaving")
	}
	// Back again on the next play.
	v.mu.Lock()
	v.people = 1
	v.mu.Unlock()
	p.add(context.Background(), tracks("two"))
	waitFor(t, "a second call", func() bool { v.mu.Lock(); defer v.mu.Unlock(); return len(v.calls) == 2 })
}

func TestPlayerLeavesWhenQuiet(t *testing.T) {
	v := &fakeVoice{people: 2}
	p := newTestPlayer(v, 50*time.Millisecond)
	p.add(context.Background(), tracks("one"))
	waitFor(t, "one", func() bool { return strings.Contains(v.saidAll(), "Now playing: one") })
	// Paused counts as playing: no leaving.
	p.setPaused(true)
	time.Sleep(150 * time.Millisecond)
	if strings.Contains(v.saidAll(), "Left voice") {
		t.Fatal("left while paused")
	}
	v.calls[0].current().finish(nil)
	waitFor(t, "it to leave", func() bool { return strings.Contains(v.saidAll(), "Left voice: nothing played") })
}

func TestPlayerTakenOutOfVoice(t *testing.T) {
	v := &fakeVoice{people: 1}
	p := newTestPlayer(v, time.Hour)
	p.add(context.Background(), tracks("one", "two"))
	waitFor(t, "one", func() bool { return strings.Contains(v.saidAll(), "Now playing: one") })
	v.calls[0].end("left") // an admin's voice leave
	waitFor(t, "the queue to clear", func() bool { cur, _, next := p.list(); return cur == nil && len(next) == 0 })
}

func TestPlayerVoiceFull(t *testing.T) {
	v := &fakeVoice{people: 8, joinErr: errors.New("the voice room is full (8/8)")}
	p := newTestPlayer(v, time.Hour)
	p.add(context.Background(), tracks("one"))
	waitFor(t, "the complaint", func() bool { return strings.Contains(v.saidAll(), "Couldn't join voice") })
}

func TestPlayerExit(t *testing.T) {
	v := &fakeVoice{people: 1}
	p := newTestPlayer(v, time.Hour)
	if p.exit(context.Background()) {
		t.Fatal("exited when not in voice")
	}
	p.add(context.Background(), tracks("one", "two"))
	waitFor(t, "one", func() bool { return strings.Contains(v.saidAll(), "Now playing: one") })
	if !p.exit(context.Background()) {
		t.Fatal("exit said it wasn't in voice")
	}
	if v.calls[0].Why() != "left" {
		t.Fatal("didn't leave the call")
	}
	// A play right after exit isn't lost with the old call: it starts a new one.
	p.add(context.Background(), tracks("three"))
	waitFor(t, "three on a new call", func() bool {
		v.mu.Lock()
		defer v.mu.Unlock()
		return len(v.calls) == 2 && strings.Contains(strings.Join(v.said, "\n"), "Now playing: three")
	})
}
