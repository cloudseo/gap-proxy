package mtcp

import "sync"

var packetPool = NewBytesPool(MaxReceivePacketSize)

type BytesPool struct {
	space int
	cache sync.Pool
}

func NewBytesPool(space int) *BytesPool {
	return &BytesPool{
		cache: sync.Pool{
			New: func() interface{} {
				return make([]byte, space)
			},
		},
		space: space,
	}
}

func (p *BytesPool) Get() []byte {
	return p.cache.Get().([]byte)
}

func (p *BytesPool) Put(b []byte) {
	if len(b) != p.space {
		return
	}
	p.cache.Put(b)
}
