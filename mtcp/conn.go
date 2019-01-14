package mtcp

import (
	"io"
	"net"
)

type connection interface {
	WriteTo(b []byte, addr net.Addr) (int, error)
	ReadFrom(p []byte) (n int, addr net.Addr, err error)
	Close() error
	LocalAddr() net.Addr
	SetReadBuffer(bytes int) error
	SetWriteBuffer(bytes int) error
}

type clientConn struct {
	conn *net.UDPConn
}

func newClientConn(c *net.UDPConn) *clientConn {
	return &clientConn{
		conn: c,
	}
}

func (c *clientConn) WriteTo(b []byte, _ net.Addr) (n int, err error) {
	return c.conn.Write(b)
}

func (c *clientConn) ReadFrom(p []byte) (n int, addr net.Addr, err error) {
	n, err = c.conn.Read(p)
	return n, c.conn.RemoteAddr(), err
}

func (c *clientConn) Close() (err error) {
	return c.conn.Close()
}

func (c *clientConn) LocalAddr() net.Addr {
	return c.conn.LocalAddr()
}

func (c *clientConn) SetReadBuffer(bytes int) (err error) {
	return c.conn.SetReadBuffer(bytes)
}

func (c *clientConn) SetWriteBuffer(bytes int) (err error) {
	return c.conn.SetWriteBuffer(bytes)
}

type secureConn struct {
	conn   connection
	cipher *Cipher
}

func newSecureConn(c connection, cipher *Cipher) connection {
	return &secureConn{conn: c, cipher: cipher}
}

func (s *secureConn) WriteTo(b []byte, addr net.Addr) (n int, err error) {
	cipher := s.cipher.Clone(FlagEncrypt)
	cipher.Encrypt(b, b)
	return s.conn.WriteTo(b, addr)
}

func (s *secureConn) ReadFrom(p []byte) (n int, addr net.Addr, err error) {
	n, addr, err = s.conn.ReadFrom(p)

	if err != nil && err != io.EOF {
		return
	}

	if n > 0 {
		cipher := s.cipher.Clone(FlagDecrypt)
		cipher.Decrypt(p[:n], p[:n])
	}

	return
}

func (s *secureConn) Close() (err error) {
	return s.conn.Close()
}

func (s *secureConn) LocalAddr() net.Addr {
	return s.conn.LocalAddr()
}

func (s *secureConn) SetReadBuffer(bytes int) (err error) {
	return s.conn.SetReadBuffer(bytes)
}

func (s *secureConn) SetWriteBuffer(bytes int) (err error) {
	return s.conn.SetWriteBuffer(bytes)
}
