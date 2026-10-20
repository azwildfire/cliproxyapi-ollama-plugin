// Package transport abstracts the host-provided HTTP and stream callbacks so the provider
// logic can be unit tested without the C ABI.
package transport

import (
	"context"
	"net/http"
)

// Request is an upstream HTTP request issued through the host.
type Request struct {
	Method  string
	URL     string
	Headers http.Header
	Body    []byte
}

// Response is a buffered upstream HTTP response.
type Response struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
}

// Stream is an open upstream HTTP stream owned by the host.
type Stream struct {
	StatusCode int
	Headers    http.Header
	ID         string
}

// StreamChunk is one upstream stream read result.
type StreamChunk struct {
	Payload []byte
	Error   string
	Done    bool
}

// Host is the subset of host callbacks the provider needs.
type Host interface {
	// Do performs a buffered HTTP request through the host transport policy.
	Do(ctx context.Context, callbackID string, req Request) (Response, error)
	// OpenStream performs a streaming HTTP request and returns a host-owned stream.
	OpenStream(ctx context.Context, callbackID string, req Request) (Stream, error)
	// ReadStream reads the next chunk from a host-owned upstream stream.
	ReadStream(ctx context.Context, streamID string) (StreamChunk, error)
	// CloseStream releases a host-owned upstream stream.
	CloseStream(ctx context.Context, streamID string) error
	// Emit forwards one output chunk to the client stream identified by streamID.
	Emit(ctx context.Context, streamID string, payload []byte) error
	// CloseOutput finishes the client stream, with an optional terminal error message.
	CloseOutput(ctx context.Context, streamID, message string)
}
