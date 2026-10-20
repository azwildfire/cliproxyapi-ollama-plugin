package sse

import "testing"

func TestDecoderSplitsFrames(t *testing.T) {
	decoder := &Decoder{}
	frames := decoder.Feed([]byte("data: {\"a\":1}\n\nevent: ping\ndata: {\"b\""))
	if len(frames) != 1 || string(frames[0]) != "data: {\"a\":1}\n\n" {
		t.Fatalf("unexpected frames %q", frames)
	}
	frames = decoder.Feed([]byte(":2}\r\n\r\n"))
	if len(frames) != 1 || string(frames[0]) != "event: ping\ndata: {\"b\":2}\r\n\r\n" {
		t.Fatalf("unexpected second frame %q", frames)
	}
	if trailing := decoder.Flush(); trailing != nil {
		t.Fatalf("unexpected trailing %q", trailing)
	}
	decoder.Feed([]byte("data: [DONE]"))
	if trailing := decoder.Flush(); string(trailing) != "data: [DONE]" {
		t.Fatalf("flush should return the partial frame, got %q", trailing)
	}
}
