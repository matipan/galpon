package screen

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"time"
)

// maxMessage is the longest message that a screen reads. A longer sequence is
// output.
const maxMessage = 64 << 10

// pause is how long Filter keeps bytes that can start a message, when no more
// output comes.
const pause = 20 * time.Millisecond

// Filter copies src to dst until src ends, without the messages of the host.
// It calls handle for each message with token, in order. It drops a message
// with another token or bad JSON, because only this command of the screen
// knows the token. After dst fails, Filter reads src to its end, so that the
// writer does not stop, and returns the error of dst.
func Filter(dst io.Writer, src io.Reader, token string, handle func(Message)) error {
	f := &filter{out: dst, token: token, handle: handle}
	chunks := make(chan []byte)
	ended := make(chan error, 1)
	go func() {
		for {
			buffer := make([]byte, 32<<10)
			n, err := src.Read(buffer)
			if n > 0 {
				chunks <- buffer[:n]
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					err = nil
				}
				ended <- err
				close(chunks)
				return
			}
		}
	}()
	wait := time.NewTimer(pause)
	wait.Stop()
	for {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				readErr := <-ended
				f.flush()
				if f.err != nil {
					return f.err
				}
				return readErr
			}
			f.write(chunk)
			if f.holdsPart() {
				wait.Reset(pause)
			} else {
				wait.Stop()
			}
		case <-wait.C:
			if f.holdsPart() {
				f.flush()
			}
		}
	}
}

type filter struct {
	out    io.Writer
	token  string
	handle func(Message)
	// held is the start of a message, or the end of the output when it can
	// start a message.
	held []byte
	err  error
}

func (f *filter) write(chunk []byte) {
	f.held = append(f.held, chunk...)
	for {
		start := bytes.Index(f.held, []byte(prefix))
		if start < 0 {
			keep := partOfPrefix(f.held)
			f.emit(f.held[:len(f.held)-keep])
			f.held = shift(f.held, len(f.held)-keep)
			return
		}
		f.emit(f.held[:start])
		f.held = shift(f.held, start)
		end := bytes.IndexByte(f.held[len(prefix):], '\a')
		if end < 0 {
			if len(f.held) <= maxMessage {
				return
			}
			// Too long for a message: it is output.
			f.emit(f.held[:len(prefix)])
			f.held = shift(f.held, len(prefix))
			continue
		}
		f.deliver(f.held[len(prefix) : len(prefix)+end])
		f.held = shift(f.held, len(prefix)+end+1)
	}
}

// holdsPart reports whether f keeps bytes that can start a message, but not
// a whole prefix yet.
func (f *filter) holdsPart() bool {
	return len(f.held) > 0 && !bytes.HasPrefix(f.held, []byte(prefix))
}

// flush writes the kept bytes as output.
func (f *filter) flush() {
	f.emit(f.held)
	f.held = f.held[:0]
}

func (f *filter) deliver(body []byte) {
	token, data, ok := bytes.Cut(body, []byte(";"))
	if !ok || string(token) != f.token {
		return
	}
	var message Message
	if json.Unmarshal(data, &message) != nil {
		return
	}
	f.handle(message)
}

func (f *filter) emit(data []byte) {
	if len(data) == 0 || f.err != nil {
		return
	}
	if _, err := f.out.Write(data); err != nil {
		f.err = err
	}
}

// partOfPrefix returns the length of the longest end of data that is the
// start of a prefix.
func partOfPrefix(data []byte) int {
	for n := min(len(data), len(prefix)-1); n > 0; n-- {
		if bytes.HasSuffix(data, []byte(prefix[:n])) {
			return n
		}
	}
	return 0
}

// shift removes the first n bytes of data, in place.
func shift(data []byte, n int) []byte {
	copied := copy(data, data[n:])
	return data[:copied]
}
