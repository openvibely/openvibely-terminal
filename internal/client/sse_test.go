package client

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestScanSSEFramesRecognizesBareCRAndMixedLineEndings(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		reader       func(string) io.Reader
		mode         sseDataLineMode
		wantNames    []string
		wantPayloads []string
	}{
		{
			name:         "bare CR",
			input:        "event: first\rdata:  hello\r\r",
			reader:       func(input string) io.Reader { return strings.NewReader(input) },
			mode:         sseDataLineChatOutput,
			wantNames:    []string{"first"},
			wantPayloads: []string{" hello"},
		},
		{
			name: "mixed CR LF and CRLF split across reads",
			input: "event: first\r\ndata:  alpha\n\r" +
				"event: second\rdata:  beta\r\n\r\n",
			reader:       func(input string) io.Reader { return oneByteReader{reader: strings.NewReader(input)} },
			mode:         sseDataLineChatOutput,
			wantNames:    []string{"first", "second"},
			wantPayloads: []string{" alpha", " beta"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var names, payloads []string
			err := scanSSEFrames(tc.reader(tc.input), tc.mode, func(frame rawSSEFrame) bool {
				names = append(names, strings.TrimSpace(frame.eventName))
				payloads = append(payloads, string(frame.payload))
				return true
			})
			if err != nil {
				t.Fatalf("scanSSEFrames: %v", err)
			}
			if !equalSSEStrings(names, tc.wantNames) {
				t.Errorf("event names = %#v, want %#v", names, tc.wantNames)
			}
			if !equalSSEStrings(payloads, tc.wantPayloads) {
				t.Errorf("payloads = %#v, want %#v", payloads, tc.wantPayloads)
			}
		})
	}
}

type oneByteReader struct {
	reader *strings.Reader
}

func (r oneByteReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return r.reader.Read(p[:1])
}

func TestScanSSEFramesPreservesConsumerDataSemantics(t *testing.T) {
	input := ": keepalive\r\n" +
		"event:  first-event  \r\n" +
		"data:  first \r\n" +
		"data:\r\n" +
		"data:  second  \r\n" +
		"data:\tkeep-tab\r\n" +
		": frame comment\r\n\r\n" +
		": another keepalive\r\n\r\n" +
		"event: second\r\n" +
		"data:  tail\r\n\r\n"

	for _, tc := range []struct {
		name         string
		mode         sseDataLineMode
		wantPayloads []string
	}{
		{
			name:         "chat removes one optional leading space",
			mode:         sseDataLineChatOutput,
			wantPayloads: []string{" first \n\n second  \n\tkeep-tab", " tail"},
		},
		{
			name:         "live trims each line",
			mode:         sseDataLineLiveEvent,
			wantPayloads: []string{"first\n\nsecond\nkeep-tab", "tail"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var names, payloads []string
			err := scanSSEFrames(strings.NewReader(input), tc.mode, func(frame rawSSEFrame) bool {
				names = append(names, strings.TrimSpace(frame.eventName))
				payloads = append(payloads, string(frame.payload))
				return true
			})
			if err != nil {
				t.Fatalf("scanSSEFrames: %v", err)
			}
			if want := []string{"first-event", "second"}; !equalSSEStrings(names, want) {
				t.Fatalf("event names = %#v, want %#v", names, want)
			}
			if !equalSSEStrings(payloads, tc.wantPayloads) {
				t.Fatalf("payloads = %#v, want %#v", payloads, tc.wantPayloads)
			}
		})
	}
}

func TestScanSSEFramesPayloadOwnershipAcrossFrames(t *testing.T) {
	input := "event: first\ndata: original payload\n\n" +
		"event: second\ndata: replacement payload\n\n" +
		"event: third\ndata: final payload\n\n"
	var events []Event
	err := scanSSEFrames(strings.NewReader(input), sseDataLineLiveEvent, func(frame rawSSEFrame) bool {
		events = append(events, Event{
			Name: strings.TrimSpace(frame.eventName),
			Data: json.RawMessage(frame.payload),
		})
		return true
	})
	if err != nil {
		t.Fatalf("scanSSEFrames: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}
	wantNames := []string{"first", "second", "third"}
	wantPayloads := []string{"original payload", "replacement payload", "final payload"}
	for i, event := range events {
		if event.Name != wantNames[i] || !bytes.Equal(event.Data, []byte(wantPayloads[i])) {
			t.Errorf("event %d = (%q, %q), want (%q, %q)", i, event.Name, event.Data, wantNames[i], wantPayloads[i])
		}
	}
}

func equalSSEStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
