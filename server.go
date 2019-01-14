package main

import (
	"io"
	"net"

	"time"

	"github.com/fanpei91/gap/mtcp"
	"github.com/pkg/errors"
)

func newServer(listenAddr string, key string) *server {
	return &server{
		key:        key,
		listenAddr: listenAddr,
	}
}

func (r *server) listen() (err error) {
	var cipher *mtcp.Cipher
	var listener *mtcp.Listener

	if cipher, err = mtcp.NewCipher([]byte(r.key)); err != nil {
		err = errors.WithStack(err)
		return
	}
	if listener, err = mtcp.Listen(r.listenAddr, cipher, 0); err != nil {
		err = errors.WithStack(err)
		return
	}

	var conn *mtcp.Conn
	for {
		if conn, err = listener.Accept(); err != nil {
			err = errors.WithStack(err)
			return
		}
		go r.handleConn(conn)
	}
	return
}

func (r *server) handleConn(conn *mtcp.Conn) {
	/* header
	   +------+----------+----------+
	   | ATYP | DST.ADDR | DST.PORT |
	   +------+----------+----------+
	   |  1   | Variable |    2     |
	   +------+----------+----------+
	*/
	buf := make([]byte, 259)
	defer conn.Close()

	if _, err := io.ReadFull(conn, buf[:1]); err != nil {
		printLog(errors.WithStack(err))
		return
	}

	var addrStart, addrEnd int
	switch buf[0] {
	case typeIPv4:
		addrStart = 1
		addrEnd = addrStart + net.IPv4len + 2
	case typeDomain:
		if _, err := io.ReadFull(conn, buf[1:2]); err != nil {
			printLog(errors.WithStack(err))
			return
		}
		addrStart = 2
		addrEnd = addrStart + int(buf[1]) + 2
	case typeIPv6:
		addrStart = 1
		addrEnd = addrStart + net.IPv6len + 2
	default:
		return
	}

	if _, err := io.ReadFull(conn, buf[addrStart:addrEnd]); err != nil {
		printLog(errors.WithStack(err))
		return
	}
	destAddr, err := parseDestAddr(buf[:addrEnd])
	if err != nil {
		printLog(err)
		return
	}

	c, err := net.DialTimeout("tcp", destAddr, 3*time.Second)
	if err != nil {
		printLog(errors.WithStack(err))
		return
	}

	defer c.Close()

	go pipe(conn, c)
	pipe(c, conn)
}
