package mtcp

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
)

type CipherFlag int

const (
	FlagEncrypt CipherFlag = 1 << iota
	FlagDecrypt
)

type Cipher struct {
	dec   cipher.Stream
	enc   cipher.Stream
	block cipher.Block
	iv    []byte
}

func NewCipher(key []byte) (c *Cipher, err error) {
	key = evpkey(key)
	c = &Cipher{}

	var block cipher.Block
	if block, err = aes.NewCipher(key); err != nil {
		return
	}

	iv := make([]byte, block.BlockSize())
	copy(iv, key)

	c.enc = cipher.NewCFBEncrypter(block, iv)
	c.dec = cipher.NewCFBDecrypter(block, iv)
	c.block = block
	c.iv = iv

	return
}

func (c *Cipher) Encrypt(out []byte, input []byte) {
	c.enc.XORKeyStream(out, input)
}

func (c *Cipher) Decrypt(out []byte, input []byte) {
	c.dec.XORKeyStream(out, input)
}

func (c *Cipher) Clone(flag CipherFlag) *Cipher {
	nc := *c

	if (flag & FlagEncrypt) != 0 {
		nc.enc = cipher.NewCFBEncrypter(c.block, c.iv)
	}

	if (flag & FlagDecrypt) != 0 {
		nc.dec = cipher.NewCFBDecrypter(c.block, c.iv)
	}

	return &nc
}

func evpkey(key []byte) []byte {
	const md5Len, aesKeyLen = 16, 32

	m := make([]byte, aesKeyLen)
	copy(m, md5sum(key))

	d := make([]byte, md5Len+len(key))
	copy(d, m[:md5Len])
	copy(d[md5Len:], key)
	copy(m[md5Len:], md5sum(d))

	return m
}

func md5sum(b []byte) []byte {
	m := md5.New()
	m.Write(b)
	return m.Sum(nil)
}
