package mtcp

import "sync"

const (
	maxWakers = 128
)

type sleeper struct {
	wakers chan *waker
	seen   map[int]struct{}
	mu     sync.Mutex
}

func newSleeper() *sleeper {
	return &sleeper{
		wakers: make(chan *waker, maxWakers),
		seen:   make(map[int]struct{}, maxWakers),
	}
}

func (s *sleeper) add(w *waker, id int) {
	w.id = id
	w.sleeper = s
}

func (s *sleeper) remove(w *waker) {
	s.mu.Lock()
	delete(s.seen, w.id)
	s.mu.Unlock()
}

func (s *sleeper) wakeBy(w *waker) {
	s.mu.Lock()
	if _, ok := s.seen[w.id]; !ok {
		select {
		case s.wakers <- w:
			s.seen[w.id] = struct{}{}
		default:
		}
	}
	s.mu.Unlock()
}

type waker struct {
	id      int
	sleeper *sleeper
}

func (w *waker) assert() {
	if w.sleeper == nil {
		return
	}
	w.sleeper.wakeBy(w)
}
