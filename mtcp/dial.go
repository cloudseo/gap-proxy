package mtcp

import (
	"crypto/rand"
	"encoding/binary"
	"net"
)

func generateConnID() uint64 {
	id := make([]byte, 8)
	rand.Read(id)
	return binary.BigEndian.Uint64(id)
}

func Dial(address string, cipher *Cipher) (c *Conn, err error) {
	if cipher == nil {
		err = ErrNoCipher
		return
	}

	var conn connection
	var uc net.Conn
	if uc, err = net.Dial("udp", address); err != nil {
		return
	}

	conn = newSecureConn(newClientConn(uc.(*net.UDPConn)), cipher)
	c = newEndpoint(generateConnID(), conn, nil, uc.RemoteAddr(), uc.LocalAddr())

	go func() {
		for {
			buf := packetPool.Get()
			n, _, err := conn.ReadFrom(buf)
			if n >= HeaderSize && err == nil {
				seg, err := parseSegment(buf[:n])
				if err == nil && seg.connID == c.connID {
					c.deliverSegment(seg)
				} else {
					packetPool.Put(buf)
				}
			} else if err != nil {
				packetPool.Put(buf)
				return
			}
		}
	}()

	c.connect()
	return
}
