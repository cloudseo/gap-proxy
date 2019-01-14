package mtcp

import (
	"container/heap"
	"sort"
	"sync"
)

type segmentHeap []*segment

func (h segmentHeap) Len() int {
	return len(h)
}

func (h segmentHeap) Less(i, j int) bool {
	return h[i].num.lessThan(h[j].num)
}

func (h segmentHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
}

func (h *segmentHeap) Push(x interface{}) {
	*h = append(*h, x.(*segment))
}

func (h *segmentHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

type sack struct {
	seen map[seqnum]bool
	heap segmentHeap
	size int
	used int
	pool sync.Pool
}

func (s *sack) push(seg *segment) bool {
	if s.seen[seg.num] || s.used >= s.size {
		return false
	}
	heap.Push(&s.heap, seg)
	s.seen[seg.num] = true
	s.used++
	return true
}

func (s *sack) pop() {
	seg := heap.Pop(&s.heap)
	if seg == nil {
		return
	}
	delete(s.seen, seg.(*segment).num)
	s.used--
}

func (s *sack) peek() *segment {
	return s.heap[0]
}

func (s *sack) len() int {
	return s.heap.Len()
}

func (s *sack) marshal() []seqnum {
	if s.len() == 0 {
		return nil
	}
	if s.len() == 1 {
		return []seqnum{s.heap[0].num, s.heap[0].num}
	}

	ordered := s.pool.Get().([]seqnum)[:0]
	for _, seg := range s.heap {
		ordered = append(ordered, seg.num)
	}
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].lessThan(ordered[j])
	})

	out := s.pool.Get().([]seqnum)[:0]
	last := ordered[0]
	out = append(out, last)
	for _, v := range ordered[1:] {
		if v == (last + 1) {
			last = v
			continue
		}
		out = append(out, last)
		last = v
		out = append(out, last)
	}

	s.pool.Put(ordered[:cap(ordered)])

	if len(out)%2 != 0 {
		out = append(out, last)
	}

	maxNumSACK := MaxSegmentSize / 4
	if (maxNumSACK % 2) != 0 {
		maxNumSACK--
	}

	out = out[:min(len(out), maxNumSACK)]

	return out
}

type receiver struct {
	ep     *endpoint
	closed bool
	rcvNxt seqnum
	sack   sack
	acked  bool
}

func newReceiver(ep *endpoint, wnd int) *receiver {
	r := &receiver{
		ep:     ep,
		rcvNxt: iss,
		sack: sack{
			size: wnd,
			seen: make(map[seqnum]bool, wnd),
			pool: sync.Pool{
				New: func() interface{} {
					return make([]seqnum, wnd)
				},
			},
		},
	}
	return r
}

func (r *receiver) handleRcvdSegment(seg *segment) (rcvd bool) {
	if r.closed {
		return false
	}

	segLen := seg.dataSize()
	segSeq := seg.num
	if !r.acceptable(segSeq, segLen) {
		r.acked = true
		r.ep.snd.sendAck()
		return false
	}

	if !r.consumeSegment(seg, segSeq, segLen) {
		if segLen > 0 || seg.flagIsSet(flagFin) {
			r.sack.push(seg)
		}
		return true
	}

	for !r.closed && r.sack.len() > 0 {
		seg := r.sack.peek()
		segLen := seg.dataSize()
		segSeq := seg.num
		if !segSeq.lessThan(r.rcvNxt) && !r.consumeSegment(seg, segSeq, segLen) {
			break
		}
		r.sack.pop()
	}

	return true
}

func (r *receiver) consumeSegment(seg *segment, segSeq seqnum, segLen int) bool {
	if segLen > 0 && segSeq == r.rcvNxt {
		r.rcvNxt++
		r.ep.readyToRead(seg)
	} else if segSeq != r.rcvNxt {
		return false
	}
	if seg.flagIsSet(flagFin) && segLen == 0 {
		r.rcvNxt++
		r.acked = true
		r.ep.snd.sendAck()
		r.closed = true
		r.ep.readyToRead(nil)
	} else if seg.flagIsSet(flagCnt) {
		r.rcvNxt++
		r.ep.deliver()
	}
	return true
}

func (r *receiver) getSendParams() (rcvNxt seqnum, rcvWnd uint16, sack []seqnum) {
	rcvWnd = uint16(r.ep.getRcvWnd())
	sack = r.sack.marshal()
	return r.rcvNxt, rcvWnd, sack
}

func (r *receiver) acceptable(segSeq seqnum, segLen int) bool {
	rcvWnd := r.ep.getRcvWnd()
	if rcvWnd <= 0 {
		return segLen == 0 && segSeq == r.rcvNxt
	}
	return segSeq.inWindow(r.rcvNxt, rcvWnd)
}

func (r *receiver) changeWindow(wnd int) {
	r.sack.pool = sync.Pool{
		New: func() interface{} {
			return make([]seqnum, wnd)
		},
	}
	r.sack.size = wnd
}
