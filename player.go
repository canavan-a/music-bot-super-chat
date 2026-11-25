package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/canavan-a/super-chat/shared/bot"
	"github.com/canavan-a/super-chat/shared/bot/audio"
)

// What a player needs from the bot, as interfaces so it can be tested
// without a server. In the bot, these are the SDK's Voice, Call and
// Playback (see bot.go).
type (
	voice interface {
		join(ctx context.Context, room string) (call, error)
		others(room string) int // people in the room's voice, not counting the bot
		say(ctx context.Context, room, text string)
	}
	call interface {
		Play(audio.Source) handle
		Leave(ctx context.Context) error
		Done() <-chan struct{}
		Why() string
	}
	handle interface {
		Done() <-chan struct{}
		Err() error
		Stop()
		Pause()
		Resume()
		Paused() bool
	}
	// source is a track playing: what's sent, and what stops its processes.
	source interface {
		audio.Source
		Close()
		Reason(err error) string
	}
)

// player is one chat's music: its queue, and the bot in its voice.
type player struct {
	room   string
	v      voice
	res    *Resolver
	open   func(context.Context, Track) (source, error)
	idle   time.Duration // leave after this long alone, or with nothing to play
	tick   time.Duration
	now    func() time.Time
	onExit func() // the loop stopped (out of voice, nothing queued)

	mu         sync.Mutex
	running    bool
	queue      []Track
	cur        *Track
	src        source
	h          handle
	c          call
	quietSince time.Time // nothing playing or queued since (zero: something is)
	aloneSince time.Time // nobody else in voice since (zero: someone is)
	wake       chan struct{}
}

// add queues tracks and gets playing if nothing is. It says where the first
// one landed: 0 is "next up, now".
func (p *player) add(ctx context.Context, ts []Track) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	pos := len(p.queue)
	if p.cur != nil {
		pos++
	}
	p.queue = append(p.queue, ts...)
	if !p.running {
		p.running = true
		p.wake = make(chan struct{}, 1)
		go p.loop(context.WithoutCancel(ctx))
	}
	p.poke()
	return pos
}

func (p *player) poke() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *player) loop(ctx context.Context) {
	t := time.NewTicker(p.tick)
	defer t.Stop()
	defer func() {
		p.mu.Lock()
		p.running = false
		p.mu.Unlock()
		if p.onExit != nil {
			p.onExit()
		}
	}()
	for {
		if done := p.next(ctx); done {
			return
		}
		p.mu.Lock()
		var hdone, cdone <-chan struct{}
		if p.h != nil {
			hdone = p.h.Done()
		}
		c := p.c
		if c != nil {
			cdone = c.Done()
		}
		p.mu.Unlock()
		select {
		case <-hdone:
			p.finished(ctx)
		case <-cdone:
			if p.callEnded(c) {
				return
			}
		case <-p.wake:
		case <-t.C:
			if p.idleCheck(ctx) {
				return
			}
		}
	}
}

// next starts the next queued track, if nothing's playing. It reports true
// when there's nothing more to do (no call, nothing queued).
func (p *player) next(ctx context.Context) bool {
	for {
		p.mu.Lock()
		if p.h != nil {
			p.mu.Unlock()
			return false
		}
		if len(p.queue) == 0 {
			if p.quietSince.IsZero() {
				p.quietSince = p.now()
			}
			out := p.c == nil
			p.mu.Unlock()
			return out
		}
		t := p.queue[0]
		p.queue = p.queue[1:]
		c := p.c
		p.mu.Unlock()

		if c == nil {
			var err error
			if c, err = p.v.join(ctx, p.room); err != nil {
				msg := "Couldn't join voice: " + err.Error()
				if errors.Is(err, bot.ErrVoiceFull) {
					msg = "Voice is full (8 of 8), so I can't play anything."
				}
				p.v.say(ctx, p.room, msg)
				p.mu.Lock()
				p.queue = nil
				p.mu.Unlock()
				return true
			}
			p.mu.Lock()
			p.c, p.aloneSince = c, time.Time{}
			p.mu.Unlock()
		}
		if err := p.res.ensure(ctx, &t); err != nil {
			p.v.say(ctx, p.room, "Couldn't find "+t.Title+": "+err.Error())
			continue
		}
		src, err := p.open(ctx, t)
		if err != nil {
			p.v.say(ctx, p.room, err.Error())
			continue
		}
		p.mu.Lock()
		if p.c != c { // the call ended while the track was starting
			p.mu.Unlock()
			src.Close()
			return false
		}
		p.cur, p.src, p.h = &t, src, c.Play(src)
		p.quietSince = time.Time{}
		p.mu.Unlock()
		p.v.say(ctx, p.room, "▶ Now playing: "+describe(t))
		return false
	}
}

// finished cleans up after a track ends (played out, skipped or stopped).
func (p *player) finished(ctx context.Context) {
	p.mu.Lock()
	t, h, src := p.cur, p.h, p.src
	p.cur, p.h, p.src = nil, nil, nil
	p.mu.Unlock()
	if h == nil {
		return
	}
	err := h.Err()
	src.Close()
	if err != nil && !errors.Is(err, bot.ErrReplaced) && !errors.Is(err, bot.ErrCallEnded) {
		p.v.say(ctx, p.room, "Couldn't finish "+t.Title+": "+src.Reason(err))
	}
}

// callEnded handles the bot leaving voice some other way (an admin's voice
// leave, a kick, the bot stopping). True when the loop should stop.
func (p *player) callEnded(c call) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.c != c {
		return false // already let go (exit): carry on with whatever's queued since
	}
	if p.src != nil {
		p.src.Close()
	}
	p.c, p.h, p.cur, p.src = nil, nil, nil, nil
	p.queue = nil // taken out of voice: don't come straight back
	return true
}

// idleCheck leaves voice after p.idle with nobody else in it, or with
// nothing playing or queued. A paused track counts as playing. True when it
// left.
func (p *player) idleCheck(ctx context.Context) bool {
	now := p.now()
	p.mu.Lock()
	if p.c == nil {
		p.mu.Unlock()
		return false
	}
	why := ""
	if p.v.others(p.room) == 0 {
		if p.aloneSince.IsZero() {
			p.aloneSince = now
		} else if now.Sub(p.aloneSince) >= p.idle {
			why = fmt.Sprintf("nobody was listening for %s", minutes(p.idle))
		}
	} else {
		p.aloneSince = time.Time{}
	}
	if why == "" && p.h == nil && len(p.queue) == 0 && !p.quietSince.IsZero() && now.Sub(p.quietSince) >= p.idle {
		why = fmt.Sprintf("nothing played for %s", minutes(p.idle))
	}
	if why == "" {
		p.mu.Unlock()
		return false
	}
	c, src := p.c, p.src
	p.c, p.h, p.cur, p.src, p.queue = nil, nil, nil, nil, nil
	p.mu.Unlock()
	if src != nil {
		src.Close()
	}
	_ = c.Leave(ctx)
	p.v.say(ctx, p.room, "Left voice: "+why+".")
	return true
}

// ---- controls ----

func (p *player) skip() (*Track, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.h == nil {
		return nil, false
	}
	t := p.cur
	p.h.Stop()
	return t, true
}

// exit stops the music, clears the queue and leaves voice now. False when
// the bot wasn't in voice.
func (p *player) exit(ctx context.Context) bool {
	p.mu.Lock()
	c := p.c
	// Detached here, not when the loop notices: a play right after exit
	// starts a new call instead of being cleared with this one.
	p.c, p.queue = nil, nil
	if p.h != nil {
		p.h.Stop()
	}
	p.mu.Unlock()
	if c == nil {
		return false
	}
	_ = c.Leave(ctx)
	return true
}

func (p *player) stop() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	had := p.h != nil || len(p.queue) > 0
	p.queue = nil
	if p.h != nil {
		p.h.Stop()
	}
	return had
}

// setPaused pauses or resumes; false when nothing is playing.
func (p *player) setPaused(paused bool) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.h == nil {
		return false
	}
	if paused {
		p.h.Pause()
	} else {
		p.h.Resume()
	}
	return true
}

// list is what's playing (nil if nothing), whether it's paused, and what's next.
func (p *player) list() (*Track, bool, []Track) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var cur *Track
	paused := false
	if p.cur != nil {
		c := *p.cur
		cur = &c
		paused = p.h.Paused()
	}
	return cur, paused, append([]Track(nil), p.queue...)
}

// ---- words ----

func describe(t Track) string {
	s := t.Title
	if t.Duration > 0 {
		s += " (" + clock(t.Duration) + ")"
	}
	if t.By != "" {
		s += ", queued by " + t.By
	}
	return s
}

func clock(d time.Duration) string {
	s := int(d.Round(time.Second) / time.Second)
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

func minutes(d time.Duration) string {
	if d%time.Minute == 0 {
		m := int(d / time.Minute)
		if m == 1 {
			return "a minute"
		}
		return fmt.Sprintf("%d minutes", m)
	}
	return d.String()
}
