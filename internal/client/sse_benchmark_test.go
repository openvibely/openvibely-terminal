package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const sseBenchmarkBatchSize = 16

var (
	sseBenchmarkLiveSink Event
	sseBenchmarkChatSink ChatOutputEvent
)

func BenchmarkSSEFrameConversion(b *testing.B) {
	for _, size := range []int{256, 4 * 1024, 64 * 1024} {
		for _, multiline := range []bool{false, true} {
			shape := "single"
			if multiline {
				shape = "multiline"
			}
			frameInput := sseBenchmarkFrame(size, multiline)
			input := bytes.Repeat(frameInput, sseBenchmarkBatchSize)
			for _, consumer := range []string{"live", "chat"} {
				name := fmt.Sprintf("%s/%s/%dB", consumer, shape, size)
				b.Run(name, func(b *testing.B) {
					b.ReportAllocs()
					var reader bytes.Reader
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						reader.Reset(input)
						if consumer == "live" {
							err := scanSSEFrames(&reader, sseDataLineLiveEvent, func(frame rawSSEFrame) bool {
								sseBenchmarkLiveSink = Event{
									Name: strings.TrimSpace(frame.eventName),
									Data: json.RawMessage(frame.payload),
								}
								return true
							})
							if err != nil {
								b.Fatal(err)
							}
						} else {
							err := scanSSEFrames(&reader, sseDataLineChatOutput, func(frame rawSSEFrame) bool {
								sseBenchmarkChatSink = ChatOutputEvent{
									Name: strings.TrimSpace(frame.eventName),
									Data: string(frame.payload),
								}
								return true
							})
							if err != nil {
								b.Fatal(err)
							}
						}
					}
				})
			}
		}
	}
}

func sseBenchmarkFrame(size int, multiline bool) []byte {
	payload := strings.Repeat("x", size)
	var data strings.Builder
	data.Grow(size + 32)
	data.WriteString("event: benchmark-event\n")
	if !multiline {
		data.WriteString("data: ")
		data.WriteString(payload)
		data.WriteString("\n\n")
		return []byte(data.String())
	}
	mid := len(payload) / 2
	data.WriteString("data: ")
	data.WriteString(payload[:mid])
	data.WriteString("\n")
	data.WriteString("data: ")
	data.WriteString(payload[mid:])
	data.WriteString("\n\n")
	return []byte(data.String())
}
