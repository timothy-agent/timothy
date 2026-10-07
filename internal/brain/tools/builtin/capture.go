package builtin

import "fmt"

// Shell output keeps its head and its tail (D-136, issue #1013): test
// runners print their summary last, so a head-only cap loses it.
const (
	ShellHeadBytes = 24 << 10
	ShellTailBytes = 40 << 10
)

// HeadTailWriter retains the first head bytes and the last tail bytes
// of a stream and counts the rest as dropped. Writes always succeed so
// the producing process can finish; memory stays bounded.
type HeadTailWriter struct {
	headMax, tailMax int
	head, tail       []byte
	dropped          int
}

// NewHeadTailWriter returns a writer keeping head + tail bytes at most.
func NewHeadTailWriter(head, tail int) *HeadTailWriter {
	return &HeadTailWriter{headMax: head, tailMax: tail}
}

func (w *HeadTailWriter) Write(p []byte) (int, error) {
	n := len(p)
	if room := w.headMax - len(w.head); room > 0 {
		k := min(room, len(p))
		w.head = append(w.head, p[:k]...)
		p = p[k:]
	}
	if len(p) == 0 {
		return n, nil
	}
	if len(p) >= w.tailMax {
		w.dropped += len(w.tail) + len(p) - w.tailMax
		w.tail = append(w.tail[:0], p[len(p)-w.tailMax:]...)
		return n, nil
	}
	w.tail = append(w.tail, p...)
	if over := len(w.tail) - w.tailMax; over > 0 {
		w.dropped += over
		w.tail = w.tail[:copy(w.tail, w.tail[over:])]
	}
	return n, nil
}

// Dropped reports how many bytes fell between the head and the tail.
func (w *HeadTailWriter) Dropped() int { return w.dropped }

// String returns head, a "[N bytes dropped]" marker when anything was
// dropped, then tail.
func (w *HeadTailWriter) String() string {
	if w.dropped == 0 {
		return string(w.head) + string(w.tail)
	}
	return fmt.Sprintf("%s\n[%d bytes dropped]\n%s", w.head, w.dropped, w.tail)
}
