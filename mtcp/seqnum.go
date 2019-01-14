package mtcp

type seqnum uint32

func (v seqnum) inWindow(first seqnum, size int) bool {
	return v.inRange(first, first+seqnum(size))
}

// [a, b)
func (v seqnum) inRange(a, b seqnum) bool {
	return v-a < b-a
}

func (v seqnum) lessThanEq(w seqnum) bool {
	if v == w {
		return true
	}
	return v.lessThan(w)
}

func (v seqnum) lessThan(w seqnum) bool {
	return int32(v-w) < 0
}
