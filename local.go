package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"

	"sync"

	"log"

	"github.com/fanpei91/gap-proxy/mtcp"
	"github.com/pkg/errors"
)

const (
	socks5         = 0x05
	noAuthRequired = 0x00
)

const (
	succeeded = 0x00
	rsv       = 0x00
	ipv4      = 0x01
)

const (
	connectCmd = 0x01
)

const (
	typeIPv4   = 0x01
	typeDomain = 0x03
	typeIPv6   = 0x04
)

type server struct {
	key        string
	listenAddr string
}

type localProxy struct {
	server
	serverAddr string

	mu          sync.Mutex
	connections map[uint64]*mtcp.Conn
	rcvWnd      uint16
}

func printLog(v interface{}) {
	log.Printf("%+v\n", v)
}

type destAddr struct {
	raw  []byte
	addr string
}

func newLocalProxy(listenAddr, serverAddr string, key string) *localProxy {
	return &localProxy{
		serverAddr: serverAddr,
		server: server{
			key:        key,
			listenAddr: listenAddr,
		},
		rcvWnd:      mtcp.DefaultRcvWnd,
		connections: make(map[uint64]*mtcp.Conn, 0),
	}
}

func (l *localProxy) setRcvWnd(wnd uint16) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.rcvWnd = wnd
	for _, conn := range l.connections {
		conn.SetRcvWnd(wnd)
	}
}

func (l *localProxy) getRcvWnd() uint16 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rcvWnd
}

func (l *localProxy) listen() (err error) {
	var ls net.Listener
	if ls, err = net.Listen("tcp", l.listenAddr); err != nil {
		err = errors.WithStack(err)
		return
	}

	listener := ls.(*net.TCPListener)
	var client *net.TCPConn
	for {
		if client, err = listener.AcceptTCP(); err != nil {
			err = errors.WithStack(err)
			return
		}
		go l.handleConn(client)
	}
}

func (l *localProxy) handleConn(client *net.TCPConn) {
	client.SetLinger(0)
	defer client.Close()

	if err := l.authenticate(client); err != nil {
		printLog(err)
		return
	}

	destAddr, err := l.handleRequest(client)
	if err != nil {
		printLog(err)
		return
	}

	reply := []byte{socks5, succeeded, rsv, ipv4, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	if _, err := client.Write(reply); err != nil {
		printLog(errors.WithStack(err))
		return
	}

	cipher, err := mtcp.NewCipher([]byte(l.key))
	if err != nil {
		printLog(errors.WithStack(err))
		return
	}

	server, err := mtcp.Dial(l.serverAddr, cipher)
	if err != nil {
		printLog(errors.WithStack(err))
		return
	}

	l.mu.Lock()
	server.SetRcvWnd(l.rcvWnd)
	l.connections[server.ConnectionID()] = server
	l.mu.Unlock()

	defer func() {
		l.mu.Lock()
		delete(l.connections, server.ConnectionID())
		l.mu.Unlock()

		server.Close()
	}()

	if _, err := server.Write(destAddr.raw); err != nil {
		printLog(errors.WithStack(err))
		return
	}

	go pipe(client, server)
	pipe(server, client)
}

func (l *localProxy) authenticate(client *net.TCPConn) error {
	/*
	   +----+----------+----------+
	   |VER | NMETHODS | METHODS  |
	   +----+----------+----------+
	   | 1  |    1     | 1 to 255 |
	   +----+----------+----------+
	*/
	buf := make([]byte, 257)
	n, err := io.ReadAtLeast(client, buf, 2)
	if err != nil {
		return errors.Wrap(err, "sockts auth failed")
	}

	if buf[0] != socks5 {
		return errors.New("sockts authentication version not supported")
	}

	authLen := int(buf[1]) + 2
	if n < authLen {
		if _, err := io.ReadFull(client, buf[n:authLen]); err != nil {
			return errors.Wrap(err, "read sockts autentication failed")
		}
	} else if n > authLen {
		return errors.New("sockts authentication got extra data")
	}

	if _, err := client.Write([]byte{socks5, noAuthRequired}); err != nil {
		return errors.Wrap(err, "write sockts authentication reply failed")
	}
	return nil
}

func (l *localProxy) handleRequest(client *net.TCPConn) (*destAddr, error) {
	/*
	   +----+-----+-------+------+----------+----------+
	   |VER | CMD |  RSV  | ATYP | DST.ADDR | DST.PORT |
	   +----+-----+-------+------+----------+----------+
	   | 1  |  1  | X'00' |  1   | Variable |    2     |
	   +----+-----+-------+------+----------+----------+
	*/
	buf := make([]byte, 262)
	n, err := io.ReadAtLeast(client, buf, 5)
	if err != nil {
		return nil, errors.Wrap(err, "read sockts request failed")
	}
	if buf[0] != socks5 {
		return nil, errors.New("sockts request version not supported")
	}
	if buf[1] != connectCmd {
		return nil, errors.New("sockts request command not supported")
	}

	var reqLen int
	switch buf[3] {
	case typeIPv4:
		reqLen = 10
	case typeDomain:
		reqLen = int(buf[4]) + 7
	case typeIPv6:
		reqLen = 22
	default:
		return nil, errors.New(fmt.Sprintf(`unsupported addr type "%d"`, buf[3]))
	}

	if n < reqLen {
		if _, err := io.ReadFull(client, buf[n:reqLen]); err != nil {
			return nil, errors.Wrap(err, "read sockts request failed")
		}
	} else if n > reqLen {
		return nil, errors.New("sockts request got extra data")
	}

	rawDstAddr := buf[3:reqLen]
	addr, err := parseDestAddr(rawDstAddr)
	return &destAddr{raw: rawDstAddr, addr: addr}, err
}

func pipe(dst net.Conn, src net.Conn) {
	buf := make([]byte, mtcp.MaxSegmentSize)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, err := dst.Write(buf[0:n]); err != nil {
				printLog(errors.WithStack(err))
				break
			}
		}
		if err != nil {
			break
		}
	}
	return
}

func parseDestAddr(rawDestAddr []byte) (host string, err error) {
	/*
	   +------+----------+----------+
	   | ATYP | DST.ADDR | DST.PORT |
	   +------+----------+----------+
	   |  1   | Variable |    2     |
	   +------+----------+----------+
	*/
	var destIP string
	var destPort []byte

	switch rawDestAddr[0] {
	case typeDomain:
		destIP = string(rawDestAddr[2 : 2+int(rawDestAddr[1])])
		destPort = rawDestAddr[2+int(rawDestAddr[1]) : 2+int(rawDestAddr[1])+2]
	case typeIPv4:
		destIP = net.IP(rawDestAddr[1 : 1+net.IPv4len]).String()
		destPort = rawDestAddr[1+net.IPv4len : +1+net.IPv4len+2]
	case typeIPv6:
		destIP = net.IP(rawDestAddr[1 : 1+net.IPv6len]).String()
		destPort = rawDestAddr[1+net.IPv6len : 1+net.IPv6len+2]
	default:
		err = errors.New("address type not supported")
		return
	}
	host = net.JoinHostPort(destIP, strconv.Itoa(int(binary.BigEndian.Uint16(destPort))))
	return
}
