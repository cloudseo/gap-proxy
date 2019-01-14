package mtcp

import (
	"container/list"
	"encoding/binary"
	"hash/fnv"
	"sync"
	"time"
)

const (
	hash            = 0
	connID          = 4
	num             = 12
	ack             = 16
	tsVal           = 20
	tsEcr           = 24
	window          = 28
	flagsDataOffset = 30
)

type segment struct {
	// protocol
	hash       uint32
	connID     uint64
	num        seqnum
	ack        seqnum
	tsVal      uint32
	tsEcr      uint32
	window     uint16
	flags      uint8
	dataOffset uint16
	sack       []seqnum
	data       []byte

	raw      []byte
	fastAck  int
	rto      time.Duration
	resendAt time.Time
	fr       bool
}

func parseSegment(raw []byte) (*segment, error) {
	h := binary.BigEndian.Uint32(raw[hash:])
	f := fnv.New32a()
	f.Write(raw[connID:])
	if h != f.Sum32() {
		return nil, ErrIllegalSegment
	}

	fd := binary.BigEndian.Uint16(raw[flagsDataOffset:])
	seg := &segment{
		hash:       h,
		connID:     binary.BigEndian.Uint64(raw[connID:]),
		num:        seqnum(binary.BigEndian.Uint32(raw[num:])),
		ack:        seqnum(binary.BigEndian.Uint32(raw[ack:])),
		tsVal:      binary.BigEndian.Uint32(raw[tsVal:]),
		tsEcr:      binary.BigEndian.Uint32(raw[tsEcr:]),
		window:     binary.BigEndian.Uint16(raw[window:]),
		flags:      uint8(fd >> 11),
		dataOffset: fd & 0x7ff,
	}

	seg.data = raw[seg.dataOffset:]
	seg.raw = raw

	sack := raw[HeaderSize:seg.dataOffset]
	for i := 0; i < len(sack); i += 4 {
		seg.sack = append(seg.sack, seqnum(binary.BigEndian.Uint32(sack[i:])))
	}

	return seg, nil
}

func (s *segment) serialize(buf []byte) {
	binary.BigEndian.PutUint64(buf[connID:], s.connID)
	binary.BigEndian.PutUint32(buf[num:], uint32(s.num))
	binary.BigEndian.PutUint32(buf[ack:], uint32(s.ack))
	binary.BigEndian.PutUint32(buf[tsVal:], s.tsVal)
	binary.BigEndian.PutUint32(buf[tsEcr:], s.tsEcr)
	binary.BigEndian.PutUint16(buf[window:], s.window)
	binary.BigEndian.PutUint16(buf[flagsDataOffset:], (uint16(s.flags)<<11)|s.dataOffset)

	for i, ack := range s.sack {
		binary.BigEndian.PutUint32(buf[HeaderSize+i*4:], uint32(ack))
	}

	if s.dataSize() > 0 {
		copy(buf[s.dataOffset:], s.data)
	}

	f := fnv.New32a()
	f.Write(buf[connID:])
	binary.BigEndian.PutUint32(buf[hash:], f.Sum32())
}

func (s *segment) flagIsSet(v uint8) bool {
	return (v & s.flags) != 0
}

func (s *segment) logicalSize() int {
	l := s.dataSize()
	if s.flagIsSet(flagFin) {
		l += 1
	}
	return l
}

func (s *segment) maxRcvdInSACK() seqnum {
	return s.sack[len(s.sack)-1]
}

func (s *segment) trimDataLeft(count int) []byte {
	v := s.data[:count]
	s.data = s.data[count:]
	return v
}

func (s *segment) dataSize() int {
	return len(s.data)
}

func (s *segment) free() {
	if s.raw != nil {
		packetPool.Put(s.raw[:cap(s.raw)])
	} else if s.data != nil {
		packetPool.Put(s.data[:cap(s.data)])
	}
}

type segmentQueue struct {
	mu    sync.Mutex
	list  list.List
	limit int
	used  int
}

func (q *segmentQueue) enqueue(seg *segment) bool {
	q.mu.Lock()
	r := q.used < q.limit
	if r {
		q.list.PushBack(seg)
		q.used++
	}
	q.mu.Unlock()
	return r
}

func (q *segmentQueue) dequeue() *segment {
	var seg *segment
	q.mu.Lock()
	element := q.list.Front()
	if element != nil {
		seg = element.Value.(*segment)
		q.list.Remove(element)
		q.used--
	}
	q.mu.Unlock()
	return seg
}

func (q *segmentQueue) setLimit(limit int) {
	q.mu.Lock()
	q.limit = limit
	q.mu.Unlock()
}

func (q *segmentQueue) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.list.Len()
}
