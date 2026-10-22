package client

// This file retains the SSE frame parser for parser compatibility. Production
// live event and execution transport uses the WebSocket client in websocket.go.

import (
	"bufio"
	"bytes"
	"io"
	"slices"
)

type sseDataLineMode uint8

const (
	sseDataLineChatOutput sseDataLineMode = iota
	sseDataLineLiveEvent
)

type rawSSEFrame struct {
	eventName     string
	payload       []byte
	dataLineCount int
}

func scanSSEFrames(r io.Reader, dataLineMode sseDataLineMode, handle func(rawSSEFrame) bool) error {
	lineReader := sseLineReader{reader: bufio.NewReaderSize(r, 64*1024)}
	if prefix, _ := lineReader.reader.Peek(1); len(prefix) == 1 && prefix[0] == 0xef {
		if prefix, _ := lineReader.reader.Peek(2); bytes.Equal(prefix, []byte{0xef, 0xbb}) {
			if prefix, _ := lineReader.reader.Peek(3); bytes.Equal(prefix, []byte{0xef, 0xbb, 0xbf}) {
				if _, err := lineReader.reader.Discard(3); err != nil {
					return err
				}
			}
		}
	}
	var frame rawSSEFrame
	lineBuffer := make([]byte, 0, 256)

	for {
		line, err := lineReader.readLine(&lineBuffer)
		if len(line) > 0 {
			switch {
			case bytes.HasSuffix(line, []byte{'\n'}):
				line = line[:len(line)-1]
				line = bytes.TrimSuffix(line, []byte{'\r'})
			case bytes.HasSuffix(line, []byte{'\r'}):
				line = line[:len(line)-1]
			}
			switch {
			case len(line) == 0:
				if frame.dataLineCount > 0 && !handle(frame) {
					return nil
				}
				frame = rawSSEFrame{}
			case bytes.HasPrefix(line, []byte("event:")):
				frame.eventName = string(line[len("event:"):])
			case bytes.HasPrefix(line, []byte("data:")):
				data := line[len("data:"):]
				switch dataLineMode {
				case sseDataLineChatOutput:
					if len(data) > 0 && data[0] == ' ' {
						data = data[1:]
					}
				case sseDataLineLiveEvent:
					data = bytes.TrimSpace(data)
				}
				if frame.dataLineCount == 0 {
					if len(data) > 0 && len(data) <= 128 {
						// Short multiline frames commonly split into similarly sized lines.
						// Reserve for the next line and separator to avoid growing twice.
						frame.payload = make([]byte, 0, 2*len(data)+1)
					}
					if len(lineBuffer) > 0 {
						// A long line already owns its storage; retain it as the payload
						// instead of copying it into a second allocation.
						frame.payload = data
					} else {
						frame.payload = append(frame.payload, data...)
					}
				} else {
					extra := len(data) + 1
					if extra > cap(frame.payload)-len(frame.payload) {
						frame.payload = slices.Grow(frame.payload, extra)
					}
					frame.payload = append(frame.payload, '\n')
					frame.payload = append(frame.payload, data...)
				}
				frame.dataLineCount++
			case len(line) > 0 && line[0] == ':':
				// Comment / keep-alive ping.
			}
			// A long line's buffer is either retained by the frame or no longer
			// needed, so allocate fresh scratch storage for the next line.
			if len(lineBuffer) > 0 {
				lineBuffer = nil
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// sseLineReader treats CR, LF, and CRLF as line endings. A CR ends the line
// immediately; a following LF is skipped on the next call so CRLF still forms
// one delimiter even when the pair arrives in separate reads.
type sseLineReader struct {
	reader *bufio.Reader
	skipLF bool
}

func (r *sseLineReader) readLine(scratch *[]byte) ([]byte, error) {
	*scratch = (*scratch)[:0]
	if r.skipLF {
		r.skipLF = false
		b, err := r.reader.Peek(1)
		if err != nil {
			return nil, err
		}
		if b[0] == '\n' {
			if _, err := r.reader.Discard(1); err != nil {
				return nil, err
			}
		}
	}

	for {
		available := r.reader.Buffered()
		if available == 0 {
			if _, err := r.reader.Peek(1); err != nil {
				if len(*scratch) > 0 {
					return *scratch, err
				}
				return nil, err
			}
			available = r.reader.Buffered()
		}
		buffered, err := r.reader.Peek(available)
		if err != nil {
			return nil, err
		}

		cr := bytes.IndexByte(buffered, '\r')
		lf := bytes.IndexByte(buffered, '\n')
		end := -1
		if cr >= 0 && (lf < 0 || cr < lf) {
			end = cr
		} else if lf >= 0 {
			end = lf
		}
		if end >= 0 {
			end++
			line := buffered[:end]
			if len(*scratch) > 0 {
				*scratch = append(*scratch, line...)
				line = *scratch
			}
			if _, err := r.reader.Discard(end); err != nil {
				return nil, err
			}
			if buffered[end-1] == '\r' {
				r.skipLF = true
			}
			return line, nil
		}

		*scratch = append(*scratch, buffered...)
		if _, err := r.reader.Discard(available); err != nil {
			return nil, err
		}
	}
}
