// Package sse parses server-sent event streams. Shared by the gateway
// provider drivers (reading upstream APIs) and the brain's gateway
// client (reading the gateway's own stream).
package sse

import (
	"bufio"
	"bytes"
	"io"
	"strings"
)

// Event is one server-sent event: the optional "event:" name and the
// concatenated "data:" payload.
type Event struct {
	Name string
	Data string
}

// Read iterates events, calling yield for each; return false to stop
// early. Comments and unknown fields are ignored per the SSE spec;
// multi-line data concatenates with newlines.
func Read(r io.Reader, yield func(Event) bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var name string
	var data bytes.Buffer
	flush := func() bool {
		if data.Len() == 0 {
			name = ""
			return true
		}
		ev := Event{Name: name, Data: data.String()}
		name = ""
		data.Reset()
		return yield(ev)
	}

	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if !flush() {
				return nil
			}
		case strings.HasPrefix(line, "event:"):
			name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	_ = flush()
	return nil
}
