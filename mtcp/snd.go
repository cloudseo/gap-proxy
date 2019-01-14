package mtcp

import (
	"container/list"
	"time"
)

const (
	minRTO               = 100 * time.Millisecond
	maxRTO               = 60 * time.Second
	nSkippedAckThreshold = 2
)

type rtt struct {
	srtt       time.Duration
	rttvar     time.Duration
	srttInited bool
}

const (
	// initial send sequence number
	iss = 0
)

type sender struct {
	ep          *endpoint
	sndWnd      int
	sndUna      seqnum
	sndNxt      seqnum
	maxSentAck  seqnum
	closed      bool
	writeNext   *list.Element
	writeList   list.List
	resendTimer *timer
	resendWaker waker
	rtt         rtt
	rto         time.Duration
}

func newSender(ep *endpoint, wnd int) *sender {
	s := &sender{
		ep:         ep,
		sndWnd:     wnd,
		sndUna:     iss,
		sndNxt:     iss,
		maxSentAck: 0,
		rto:        2 * minRTO,
	}
	s.resendTimer = newTimer(&s.resendWaker)
	return s
}

func (s *sender) sendData() {
	now := time.Now()
	element := s.writeNext
	for element != nil && s.sndNxt.lessThan(s.sndUna+seqnum(s.sndWnd)) {
		seg := element.Value.(*segment)
		seg.flags |= flagAck
		if seg.dataSize() == 0 && !seg.flagIsSet(flagCnt) {
			seg.flags |= flagFin
		}

		seg.rto = s.rto
		seg.resendAt = now.Add(seg.rto)
		s.sendSegment(seg)
		element = element.Next()
	}
	s.writeNext = element

	if s.sndUna != s.sndNxt {
		if s.resendTimer.disabled() {
			s.resendTimer.enable(s.rto)
		} else {
			s.resendTimer.tryCutIn(s.rto)
		}
	}
}

func (s *sender) sendSegment(seg *segment) error {
	rcvNxt, rcvWnd, sack := s.ep.rcv.getSendParams()
	seg.ack, s.maxSentAck = rcvNxt, rcvNxt
	seg.window = rcvWnd
	seg.dataOffset = HeaderSize
	seg.fastAck = 0

	if seg.dataSize() > 0 && len(sack) > 0 {
		unused := MaxSegmentSize - seg.dataSize()
		if unused >= 8 {
			numSACK := unused / 4
			if numSACK%2 != 0 {
				numSACK--
			}
			numSACK = min(numSACK, len(sack))
			seg.dataOffset += uint16(numSACK * 4)
			sack = sack[:numSACK]
		} else {
			sack = sack[:0]
		}
	} else if len(sack) > 0 {
		seg.dataOffset += uint16(len(sack) * 4)
	}

	seg.sack = sack
	s.ep.sendRaw(seg)

	if seg.num >= s.sndNxt && seg.logicalSize() > 0 {
		s.sndNxt = seg.num + 1
	}

	if sack != nil {
		s.ep.rcv.sack.pool.Put(sack[:cap(sack)])
	}

	return nil
}

func (s *sender) acceptable(seg *segment) bool {
	current := s.ep.timestamp()
	if seg.tsEcr > current {
		return false
	}
	return true
}

func (s *sender) handleRcvdSegment(seg *segment) {
	s.ep.updateRecentTimestamp(seg.tsVal, s.maxSentAck, seg.num)

	wnd := int(seg.window)
	if wnd != s.sndWnd {
		s.sndWnd = wnd
		s.ep.setSndWnd(wnd)
	}

	ack := seg.ack
	acked := (ack - 1).inRange(s.sndUna, s.sndNxt)
	if !(acked || len(seg.sack) > 0) {
		return
	}
	s.sndUna = ack

	s.updateRTO(seg)

	segmentsAcked := s.removeSegmentsBefore(ack) + s.removeSegmentsIn(seg.sack)
	s.ep.updateSndWndUsage(segmentsAcked)

	if len(seg.sack) > 0 {
		s.fastRetransmit(seg.maxRcvdInSACK())
	}

	s.sendData()
}

func (s *sender) removeSegmentsBefore(ack seqnum) (segmentsAcked int) {
	var next *list.Element
	for element := s.writeList.Front(); element != nil; element = next {
		seg := element.Value.(*segment)
		next = element.Next()

		if seg.num.lessThan(ack) {
			s.writeList.Remove(element)
			segmentsAcked++
			seg.free()
			continue
		}

		break
	}
	return
}

func (s *sender) removeSegmentsIn(sack []seqnum) (segmentsAcked int) {
	var next *list.Element
	for i := 0; i < len(sack); i += 2 {
		start, end := sack[i], sack[i+1]
		for element := s.writeList.Front(); element != nil; element = next {
			next = element.Next()
			seg := element.Value.(*segment)

			if start.lessThanEq(seg.num) && seg.num.lessThanEq(end) {
				s.writeList.Remove(element)
				segmentsAcked++
				seg.free()
				continue
			}

			if end.lessThan(seg.num) {
				break
			}

			seg.fastAck++
		}
	}
	return
}

func (s *sender) fastRetransmit(end seqnum) {
	now := time.Now()
	for element := s.writeList.Front(); element != nil; element = element.Next() {
		seg := element.Value.(*segment)

		if seg.num.lessThan(end) && seg.fastAck >= nSkippedAckThreshold && !seg.fr {
			seg.rto = s.rto
			seg.resendAt = now.Add(seg.rto)
			s.sendSegment(seg)
			seg.fr = true
			continue
		}

		if end.lessThanEq(seg.num) {
			break
		}
	}
}

func (s *sender) retransmit() bool {
	var closestResendAt time.Time
	now := time.Now()

	for element := s.writeList.Front(); element != nil; element = element.Next() {
		seg := element.Value.(*segment)
		if seg.resendAt.IsZero() {
			break
		}

		if seg.resendAt.Before(now) {
			if seg.rto >= maxRTO {
				return false
			}

			seg.rto *= 2
			seg.resendAt = now.Add(s.rto)
			s.sendSegment(seg)
		}

		if closestResendAt.IsZero() || seg.resendAt.Before(closestResendAt) {
			closestResendAt = seg.resendAt
		}
	}

	if s.sndUna != s.sndNxt {
		var d = s.rto
		if !closestResendAt.IsZero() {
			d = closestResendAt.Sub(now)
			if d <= 0 {
				d = s.rto
			}
		}

		if s.resendTimer.disabled() {
			s.resendTimer.enable(d)
		} else {
			s.resendTimer.tryCutIn(d)
		}
	}

	s.sendData()
	return true
}

func (s *sender) updateRTO(seg *segment) {
	// https://tools.ietf.org/html/rfc6298
	rtt := time.Duration(s.ep.timestamp()-seg.tsEcr) * time.Millisecond

	if !s.rtt.srttInited {
		s.rtt.rttvar = rtt / 2
		s.rtt.srtt = rtt
		s.rtt.srttInited = true
	} else {
		diff := s.rtt.srtt - rtt
		if diff < 0 {
			diff = -diff
		}

		const alpha = 0.125
		const beta = 0.25

		rttVar := (1-beta)*s.rtt.rttvar.Seconds() + beta*diff.Seconds()
		srtt := (1-alpha)*s.rtt.srtt.Seconds() + alpha*rtt.Seconds()

		s.rtt.rttvar = time.Duration(rttVar * float64(time.Second))
		s.rtt.srtt = time.Duration(srtt * float64(time.Second))
	}

	s.rto = s.rtt.srtt + 4*s.rtt.rttvar
	if s.rto < minRTO {
		s.rto = minRTO
	}
}

func (s *sender) sendAck() {
	s.sendSegment(&segment{
		num:   s.sndNxt,
		flags: flagAck,
	})
}
