package mtcp

import (
	"sync"
	"time"
)

type timer struct {
	enabled       bool
	target        time.Time
	runtimeTarget time.Time
	timer         *time.Timer
	mu            sync.Mutex
}

func newTimer(w *waker) *timer {
	t := timer{
		enabled: false,
	}
	t.timer = time.AfterFunc(time.Hour, func() {
		t.mu.Lock()
		if t.enabled == false {
			t.mu.Unlock()
			return
		}

		now := time.Now()
		if now.Before(t.target) {
			t.runtimeTarget = t.target
			t.timer.Reset(t.target.Sub(now))
			t.mu.Unlock()
			return
		}

		t.enabled = false
		t.mu.Unlock()
		w.assert()
	})
	t.timer.Stop()
	return &t
}

func (t *timer) disable() {
	t.mu.Lock()
	t.enabled = false
	t.mu.Unlock()
}

func (t *timer) enable(d time.Duration) {
	t.mu.Lock()
	t.target = time.Now().Add(d)
	if !t.enabled || t.target.Before(t.runtimeTarget) {
		t.runtimeTarget = t.target
		t.timer.Reset(d)
	}
	t.enabled = true
	t.mu.Unlock()
}

func (t *timer) tryCutIn(d time.Duration) {
	t.mu.Lock()
	left := t.target.Sub(time.Now())
	t.mu.Unlock()
	if d < left && d > 0 {
		t.enable(d)
	}
}

func (t *timer) disabled() bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	return !t.enabled
}

func (t *timer) cleanup() {
	t.timer.Stop()
	t.disable()
}
