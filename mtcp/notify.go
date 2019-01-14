package mtcp

type notifier struct {
	c chan struct{}
}

func newNotifier() *notifier {
	return &notifier{
		c: make(chan struct{}, 1),
	}
}

func (n *notifier) wait() <-chan struct{} {
	return n.c
}

func (n *notifier) singal() {
	select {
	case n.c <- struct{}{}:
	default:
	}
}
