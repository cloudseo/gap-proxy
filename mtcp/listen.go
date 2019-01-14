package mtcp

import (
	"net"
	"sync"
)

type Listener struct {
	mu          sync.Mutex
	connections map[uint64]*endpoint
	conn        connection
	backlog     int
	chAccept    chan *endpoint
	chError     chan error
}

func Listen(address string, cipher *Cipher, backlog int) (l *Listener, err error) {
	if cipher == nil {
		err = ErrNoCipher
		return
	}

	var c net.PacketConn
	if c, err = net.ListenPacket("udp", address); err != nil {
		return
	}

	l = &Listener{
		connections: make(map[uint64]*endpoint, backlog),
		conn:        newSecureConn(c.(*net.UDPConn), cipher),
		backlog:     backlog,
		chAccept:    make(chan *endpoint, backlog),
		chError:     make(chan error, backlog),
	}

	go l.doListen()

	return
}

func (l *Listener) doListen() {
	for {
		buf := packetPool.Get()
		if n, remote, err := l.conn.ReadFrom(buf); n >= HeaderSize && err == nil {
			var seg *segment
			if seg, err = parseSegment(buf[:n]); err != nil {
				packetPool.Put(buf)
				continue
			}

			l.mu.Lock()
			conn, found := l.connections[seg.connID]
			if !found {
				if len(l.connections) < l.backlog || l.backlog == 0 {
					conn = newEndpoint(seg.connID, l.conn, l, remote, l.conn.LocalAddr())
					l.connections[seg.connID] = conn
					conn.deliverSegment(seg)
				} else {
					seg.free()
				}
			} else {
				conn.maybeChangeRemoteAddr(remote)
				conn.deliverSegment(seg)
			}
			l.mu.Unlock()
		} else if err != nil {
			packetPool.Put(buf)
			l.chError <- err
			return
		}
	}
}

func (l *Listener) Accept() (c *Conn, err error) {
	select {
	case c = <-l.chAccept:
		return
	case err = <-l.chError:
		return
	}
}

func (l *Listener) Close() error {
	return l.conn.Close()
}

func (l *Listener) Remove(conn *Conn) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.connections, conn.connID)
}

func (l *Listener) Addr() net.Addr {
	return l.conn.LocalAddr()
}

func (l *Listener) SetReadBuffer(bytes int) error {
	return l.conn.SetReadBuffer(bytes)
}

func (l *Listener) SetWriteBuffer(bytes int) error {
	return l.conn.SetWriteBuffer(bytes)
}

func (l *Listener) addConn(c *Conn) {
	l.chAccept <- c
}
