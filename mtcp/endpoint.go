package mtcp

import (
	"container/list"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const (
	MaxReceivePacketSize = 1452 // MTU(1500) - IPv6 - UDP
	HeaderSize           = 32
	MaxSegmentSize       = MaxReceivePacketSize - HeaderSize
	DefaultRcvWnd        = 256
)

var (
	ErrTimeout           = errors.New("operation timed out")
	ErrConnectionAborted = errors.New("connection aborted")
	ErrConnectionClosed  = errors.New("connection closed")
	ErrClosedForSend     = errors.New("connection is closed for send")
	ErrClosedForReceive  = errors.New("connection is closed for receive")
	ErrConnectionReset   = errors.New("connection reset by peer")
	ErrNoCipher          = errors.New("cipher is nil")
	ErrIllegalSegment    = errors.New("illegal segment")
)

const (
	notifyKeepaliveChanged = 1 << iota
	notifyRcvWndChanged
	notifyReset
)

const (
	flagFin = 1 << iota
	flagRst
	flagAck
	flagCnt
)

const (
	maxSegmentsPerWake = 64
)

type state int

const (
	stateConnected state = iota
	stateClosed
	stateError
)

type protocolFunc struct {
	waker *waker
	fn    func() error
}

type keepalive struct {
	sync.Mutex
	enabled  bool
	idle     time.Duration
	interval time.Duration
	count    int
	unacked  int
	timer    *timer
	waker    waker
}

func min(n ...int) int {
	m := n[0]
	for _, v := range n[1:] {
		if v < m {
			m = v
		}
	}
	return m
}

type Conn = endpoint

type endpoint struct {
	mu      sync.Mutex
	state   state
	hardErr error
	rd      time.Time
	wd      time.Time
	remote  net.Addr

	sndListMu  sync.Mutex
	sndList    list.List
	sndWndSize int
	sndWndUsed int
	sndClosed  bool

	rcvListMu  sync.RWMutex
	rcvList    list.List
	rcvWndSize int
	rcvClosed  bool

	refTime           time.Time
	linger            int
	tsRecent          uint32
	keepalive         keepalive
	listener          *Listener
	local             net.Addr
	connID            uint64
	newSegmentWaker   waker
	notificationWaker waker
	sndCloseWaker     waker
	sndWaker          waker
	notifyFlags       uint32
	segmentQueue      segmentQueue
	conn              connection
	input             *notifier
	output            *notifier
	rcv               *receiver
	snd               *sender
	chDone            chan struct{}
	chExit            chan struct{}
	sndNxtNum         seqnum
}

func newEndpoint(connID uint64, conn connection, listener *Listener, remote, local net.Addr) *endpoint {
	ep := &endpoint{
		refTime:    time.Now(),
		state:      stateConnected,
		sndNxtNum:  iss,
		connID:     connID,
		listener:   listener,
		conn:       conn,
		remote:     remote,
		local:      local,
		sndWndSize: 32,
		rcvWndSize: DefaultRcvWnd,
		input:      newNotifier(),
		output:     newNotifier(),
		keepalive: keepalive{
			enabled: true,
			// Linux defaults
			idle:     2 * time.Hour,
			interval: 75 * time.Second,
			count:    9,
		},
		chDone: make(chan struct{}),
		chExit: make(chan struct{}),
		linger: -1,
	}
	ep.snd = newSender(ep, ep.sndWndSize)
	ep.rcv = newReceiver(ep, ep.rcvWndSize)
	ep.segmentQueue.setLimit(ep.rcvWndSize * 4)

	ready := make(chan struct{})
	go ep.protocolMainLoop(ready)
	<-ready

	return ep
}

func (e *endpoint) Write(b []byte) (n int, err error) {
	var pending bool
	defer func() {
		if pending && err == nil {
			e.sndWaker.assert()
		}
	}()

	length := len(b)
	for {
		e.mu.Lock()
		if e.state == stateError {
			e.mu.Unlock()
			return 0, e.hardErr
		}
		if e.state == stateClosed {
			e.mu.Unlock()
			return 0, ErrConnectionClosed
		}
		e.mu.Unlock()

		for {
			if n == length {
				return
			}

			e.sndListMu.Lock()
			if e.sndClosed {
				e.sndListMu.Unlock()
				return 0, ErrClosedForSend
			}

			if e.sndWndSize-e.sndWndUsed <= 0 {
				e.sndListMu.Unlock()
				break
			}

			buf := packetPool.Get()
			cnt := min(MaxSegmentSize, len(b))
			copy(buf[:cnt], b[:cnt])
			e.sndList.PushBack(&segment{data: buf[:cnt]})
			n += cnt
			b = b[cnt:]
			pending = true
			e.sndWndUsed++
			e.sndListMu.Unlock()
		}

		if pending {
			e.sndWaker.assert()
			pending = false
		}

		if err = e.waitDataOut(); err != nil {
			return 0, err
		}
	}
}

func (e *endpoint) waitDataOut() error {
	e.mu.Lock()
	deadline := e.wd
	e.mu.Unlock()
	return e.waitEvent(e.output, deadline)
}

func (e *endpoint) Read(b []byte) (n int, err error) {
	length := len(b)
	for {
		e.mu.Lock()
		if e.state == stateError {
			e.mu.Unlock()
			return 0, e.hardErr
		}
		if e.state == stateClosed {
			e.mu.Unlock()
			return 0, ErrConnectionClosed
		}
		e.mu.Unlock()

		for {
			if n == length {
				return
			}

			e.rcvListMu.Lock()
			if e.rcvList.Len() == 0 {
				if n > 0 {
					e.rcvListMu.Unlock()
					return
				}
				if e.rcvClosed {
					e.rcvListMu.Unlock()
					return 0, ErrClosedForReceive
				}
				e.rcvListMu.Unlock()
				break
			}

			front := e.rcvList.Front()
			seg := front.Value.(*segment)
			cnt := min(seg.dataSize(), len(b))
			copy(b[:cnt], seg.trimDataLeft(cnt))
			b = b[cnt:]
			n += cnt
			if seg.dataSize() == 0 {
				e.rcvList.Remove(front)
				seg.free()
			}
			e.rcvListMu.Unlock()
		}

		if err = e.waitDataIn(); err != nil {
			return
		}
	}
}

func (e *endpoint) waitDataIn() error {
	e.mu.Lock()
	deadline := e.rd
	e.mu.Unlock()
	return e.waitEvent(e.input, deadline)
}

func (e *endpoint) waitEvent(event *notifier, deadline time.Time) error {
	var timeout = newNotifier()
	if !deadline.IsZero() {
		duration := time.Until(deadline)
		if duration <= 0 {
			return ErrTimeout
		}
		t := time.AfterFunc(duration, timeout.singal)
		defer t.Stop()
	}
	select {
	case <-event.wait():
	case <-timeout.wait():
		return ErrTimeout
	}
	return nil
}

func (e *endpoint) setSndWnd(wnd int) {
	e.sndListMu.Lock()
	e.sndWndSize = wnd
	e.sndListMu.Unlock()
}

func (e *endpoint) SetRcvWnd(wnd uint16) {
	w := int(wnd)

	e.rcvListMu.Lock()
	changed := w != e.rcvWndSize
	e.rcvWndSize = w
	e.rcvListMu.Unlock()

	if changed {
		e.segmentQueue.setLimit(w * 4)
		e.notifyProtocolGoroutine(notifyRcvWndChanged)
	}
}

func (e *endpoint) SetLinger(sec int) error {
	e.linger = sec
	return nil
}

func (e *endpoint) SetReadBuffer(bytes int) (err error) {
	if e.listener != nil {
		return
	}
	return e.conn.SetReadBuffer(bytes)
}

func (e *endpoint) SetWriteBuffer(bytes int) (err error) {
	if e.listener != nil {
		return
	}
	return e.conn.SetWriteBuffer(bytes)
}

func (e *endpoint) SetDeadline(t time.Time) error {
	e.SetReadDeadline(t)
	e.SetWriteDeadline(t)
	return nil
}

func (e *endpoint) SetReadDeadline(t time.Time) error {
	e.mu.Lock()
	e.rd = t
	e.mu.Unlock()
	return nil
}

func (e *endpoint) SetWriteDeadline(t time.Time) error {
	e.mu.Lock()
	e.wd = t
	e.mu.Unlock()
	return nil
}

func (e *endpoint) SetKeepAlive(keepalive bool) error {
	e.keepalive.Lock()
	e.keepalive.enabled = keepalive
	e.keepalive.Unlock()
	e.notifyProtocolGoroutine(notifyKeepaliveChanged)
	return nil
}

func (e *endpoint) SetKeepAliveIdle(duration time.Duration) error {
	e.keepalive.Lock()
	e.keepalive.idle = duration
	e.keepalive.Unlock()
	e.notifyProtocolGoroutine(notifyKeepaliveChanged)
	return nil
}

func (e *endpoint) SetKeepAliveInterval(duration time.Duration) error {
	e.keepalive.Lock()
	e.keepalive.interval = duration
	e.keepalive.Unlock()
	e.notifyProtocolGoroutine(notifyKeepaliveChanged)
	return nil
}

func (e *endpoint) SetKeepAliveCount(count int) error {
	e.keepalive.Lock()
	e.keepalive.count = count
	e.keepalive.Unlock()
	e.notifyProtocolGoroutine(notifyKeepaliveChanged)
	return nil
}

func (e *endpoint) LocalAddr() net.Addr {
	return e.local
}

func (e *endpoint) RemoteAddr() net.Addr {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.remote
}

func (e *endpoint) ConnectionID() uint64 {
	return e.connID
}

func (e *endpoint) protocolMainLoop(ready chan struct{}) {
	e.keepalive.timer = newTimer(&e.keepalive.waker)
	e.resetKeepaliveTimer(false)

	epilogue := func() {
		e.keepalive.timer.cleanup()
		e.snd.resendTimer.cleanup()

		e.input.singal()
		e.output.singal()

		if e.listener != nil {
			e.listener.Remove(e)
		} else {
			e.conn.Close()
		}

		close(e.chDone)
	}

	funcs := []protocolFunc{
		{waker: &e.sndWaker, fn: e.handleWrite},
		{waker: &e.newSegmentWaker, fn: e.handleSegments},
		{waker: &e.keepalive.waker, fn: e.keepaliveTimerExpired},
		{waker: &e.sndCloseWaker, fn: e.handleClose},

		{waker: &e.snd.resendWaker, fn: func() error {
			if !e.snd.retransmit() {
				return ErrTimeout
			}
			return nil
		}},

		{waker: &e.notificationWaker, fn: func() error {
			n := e.fetchNotifications()

			if (n & notifyReset) != 0 {
				return ErrConnectionAborted
			}

			if (n & notifyKeepaliveChanged) != 0 {
				e.resetKeepaliveTimer(true)
			}

			if (n & notifyRcvWndChanged) != 0 {
				e.rcvListMu.Lock()
				wnd := e.rcvWndSize
				e.rcvListMu.Unlock()
				e.rcv.changeWindow(wnd)
			}

			return nil
		}},
	}
	s := newSleeper()
	for index, fn := range funcs {
		s.add(fn.waker, index)
	}
	close(ready)

	var exit bool
	for !e.snd.closed || !e.rcv.closed || e.snd.sndUna != e.sndNxtNum {
		select {
		case <-e.chExit:
			exit = true
		default:
		}
		if exit {
			break
		}

		select {
		case waker := <-s.wakers:
			index := waker.id
			s.remove(waker)
			if err := funcs[index].fn(); err != nil {
				e.resetConnection(err)
				exit = true
			}
		case <-e.chExit:
			exit = true
		}
		if exit {
			break
		}
	}

	e.mu.Lock()
	if e.state != stateError {
		e.state = stateClosed
	}
	e.mu.Unlock()

	epilogue()
	return
}

func (e *endpoint) Close() error {
	if err := e.shutdown(); err != nil {
		return err
	}

	linger := e.linger
	var timeout = newNotifier()
	if linger > 0 {
		duration := time.Until(time.Now().Add(time.Duration(linger) * time.Second))
		t := time.AfterFunc(duration, timeout.singal)
		defer t.Stop()
	} else if linger == 0 {
		timeout.singal()
	}

	select {
	case <-e.chDone:
		close(e.chExit)
	case <-timeout.wait():
		close(e.chExit)
		return ErrTimeout
	}

	return e.hardErr
}

func (e *endpoint) shutdown() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	switch e.state {
	case stateError:
		return e.hardErr
	case stateClosed:
		return ErrConnectionClosed
	}

	e.rcvListMu.Lock()
	rcvWndUsed := e.rcvList.Len()
	e.rcvListMu.Unlock()
	if rcvWndUsed > 0 {
		e.notifyProtocolGoroutine(notifyReset)
		return nil
	}

	e.sndListMu.Lock()
	defer e.sndListMu.Unlock()

	if e.sndClosed {
		return ErrClosedForSend
	}

	e.sndClosed = true
	e.sndList.PushBack(&segment{data: nil})
	e.sndCloseWaker.assert()

	return nil
}

func (e *endpoint) connect() {
	e.sndListMu.Lock()
	e.sndList.PushBack(&segment{
		data:  nil,
		flags: flagCnt,
	})
	e.sndListMu.Unlock()
	e.sndWaker.assert()
}

func (e *endpoint) deliver() {
	e.listener.addConn(e)
}

func (e *endpoint) notifyProtocolGoroutine(n uint32) {
	for {
		v := atomic.LoadUint32(&e.notifyFlags)
		if v&n == n {
			return
		}
		if atomic.CompareAndSwapUint32(&e.notifyFlags, v, v|n) {
			if v == 0 {
				e.notificationWaker.assert()
			}
			return
		}
	}
}

func (e *endpoint) fetchNotifications() uint32 {
	return atomic.SwapUint32(&e.notifyFlags, 0)
}

func (e *endpoint) resetConnection(err error) {
	e.mu.Lock()
	e.state = stateError
	e.hardErr = err
	e.mu.Unlock()

	if err == ErrConnectionReset {
		return
	}

	e.sendRaw(&segment{
		flags:      flagAck | flagRst,
		num:        e.snd.sndUna,
		ack:        e.rcv.rcvNxt,
		dataOffset: HeaderSize,
		window:     0,
		data:       nil,
		tsVal:      e.timestamp(),
		tsEcr:      e.tsRecent,
	})
}

func (e *endpoint) keepaliveTimerExpired() error {
	e.keepalive.Lock()
	if e.keepalive.unacked >= e.keepalive.count {
		e.keepalive.Unlock()
		return ErrConnectionReset
	}
	e.keepalive.unacked++
	e.keepalive.Unlock()

	// rfc1122, page 102: TCP keepalive is a ACK segment generally contains SEG.SEQ = SND.NXT-1
	// and may or may not contain one garbage octet of data.
	e.snd.sendSegment(&segment{num: e.snd.sndNxt - 1, flags: flagAck})
	e.resetKeepaliveTimer(false)

	return nil
}

func (e *endpoint) resetKeepaliveTimer(rcvdData bool) {
	e.keepalive.Lock()
	defer e.keepalive.Unlock()

	if rcvdData {
		e.keepalive.unacked = 0
	}

	if !e.keepalive.enabled {
		e.keepalive.timer.disable()
		return
	}

	if e.keepalive.unacked > 0 {
		e.keepalive.timer.enable(e.keepalive.interval)
	} else {
		e.keepalive.timer.enable(e.keepalive.idle)
	}
}

func (e *endpoint) deliverSegment(seg *segment) {
	if e.segmentQueue.enqueue(seg) {
		e.newSegmentWaker.assert()
	} else {
		seg.free()
	}
}

func (e *endpoint) handleSegments() (err error) {
	for i := 0; i < maxSegmentsPerWake; i++ {
		var seg *segment

		if seg = e.segmentQueue.dequeue(); seg == nil {
			break
		}

		if seg.flagIsSet(flagRst) && e.rcv.acceptable(seg.num, 0) {
			seg.free()
			err = ErrConnectionReset
			return
		}

		if !e.snd.acceptable(seg) {
			seg.free()
			e.notifyProtocolGoroutine(notifyReset)
			return
		}

		var freed bool
		if seg.dataSize() == 0 {
			freed = true
			seg.free()
		}

		rcvd := e.rcv.handleRcvdSegment(seg)
		if !rcvd && !freed {
			seg.free()
		}

		e.snd.handleRcvdSegment(seg)
	}

	if e.segmentQueue.len() > 0 {
		e.newSegmentWaker.assert()
	}

	if e.rcv.rcvNxt != e.snd.maxSentAck || !e.rcv.acked && e.rcv.sack.len() > 0 {
		e.rcv.acked = false
		e.snd.sendAck()
	}

	e.resetKeepaliveTimer(true)
	return
}

func (e *endpoint) handleClose() error {
	e.handleWrite()
	e.snd.closed = true
	return nil
}

func (e *endpoint) readyToRead(seg *segment) {
	e.rcvListMu.Lock()
	if seg != nil {
		e.rcvList.PushBack(seg)
	} else {
		e.rcvClosed = true
	}
	e.rcvListMu.Unlock()

	e.input.singal()
}

func (e *endpoint) handleWrite() error {
	e.sndListMu.Lock()
	last := e.snd.writeList.Back()
	for element := e.sndList.Front(); element != nil; element = element.Next() {
		seg := element.Value.(*segment)
		seg.num = e.sndNxtNum
		e.snd.writeList.PushBack(seg)
		e.sndNxtNum++
	}
	e.sndList.Init()
	e.sndListMu.Unlock()

	if e.snd.writeNext == nil {
		if last == nil {
			e.snd.writeNext = e.snd.writeList.Front()
		} else {
			e.snd.writeNext = last.Next()
		}
	}

	e.snd.sendData()
	return nil
}

func (e *endpoint) getRcvWnd() int {
	e.rcvListMu.RLock()
	defer e.rcvListMu.RUnlock()
	return e.rcvWndSize
}

func (e *endpoint) sendRaw(seg *segment) error {
	e.mu.Lock()
	remote := e.remote
	e.mu.Unlock()

	seg.tsVal = e.timestamp()
	seg.tsEcr = e.tsRecent
	seg.connID = e.connID
	packet := packetPool.Get()
	buf := packet[:int(seg.dataOffset)+seg.dataSize()]
	seg.serialize(buf)
	e.conn.WriteTo(buf, remote)
	packetPool.Put(packet)
	return nil
}

func (e *endpoint) timestamp() uint32 {
	return uint32(time.Now().Sub(e.refTime) / time.Millisecond)
}

func (e *endpoint) updateRecentTimestamp(tsVal uint32, lastAckSent, segSeq seqnum) {
	// rfc7323 4.3:
	// If SEG.TSval >= TS.Recent and SEG.SEQ <= Last.ACK.sent,
	// then SEG.TSval is copied to TS.Recent,
	// otherwise, it is ignored.
	if seqnum(e.tsRecent).lessThanEq(seqnum(tsVal)) && segSeq.lessThanEq(lastAckSent) || e.tsRecent == 0 {
		e.tsRecent = tsVal
	}
}

func (e *endpoint) updateSndWndUsage(segmentsAcked int) {
	if segmentsAcked == 0 {
		return
	}

	e.sndListMu.Lock()
	e.sndWndUsed -= segmentsAcked
	e.sndListMu.Unlock()

	e.output.singal()
}

func (e *endpoint) maybeChangeRemoteAddr(remote net.Addr) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.remote = remote
}
