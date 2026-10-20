package provider

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestEndpointForFormat(t *testing.T) {
	cases := map[string]string{
		"openai":           endpointChatCompletions,
		"OpenAI":           endpointChatCompletions,
		"":                 endpointChatCompletions,
		"claude":           endpointMessages,
		"anthropic":        endpointMessages,
		"openai-response":  endpointResponses,
		"openai-responses": endpointResponses,
		"responses":        endpointResponses,
	}
	for input, want := range cases {
		endpoint, ok := endpointForFormat(normalizeFormat(input))
		if !ok || endpoint != want {
			t.Errorf("format %q -> %q (ok=%v), want %q", input, endpoint, ok, want)
		}
	}
	if _, ok := endpointForFormat(normalizeFormat("gemini")); ok {
		t.Fatalf("gemini must not map to an endpoint")
	}
}

func TestPrepareBody(t *testing.T) {
	payload := []byte(`{"model":"alias","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	body, err := prepareBody(payload, "glm-5.3-flash(high)", true, formatOpenAI)
	if err != nil {
		t.Fatalf("prepareBody: %v", err)
	}
	if got := gjson.GetBytes(body, "model").String(); got != "glm-5.3-flash" {
		t.Fatalf("model = %q", got)
	}
	if !gjson.GetBytes(body, "stream").Bool() {
		t.Fatalf("stream flag not forced on")
	}
	if !gjson.GetBytes(body, "stream_options.include_usage").Bool() {
		t.Fatalf("stream_options.include_usage not added for openai streams")
	}

	claude, err := prepareBody([]byte(`{"model":"x","max_tokens":5,"messages":[]}`), "kimi-k3", false, formatClaude)
	if err != nil {
		t.Fatalf("prepareBody claude: %v", err)
	}
	if gjson.GetBytes(claude, "stream_options").Exists() {
		t.Fatalf("stream_options must not be added to Claude payloads")
	}
	if gjson.GetBytes(claude, "stream").Bool() {
		t.Fatalf("stream must be false for buffered requests")
	}
	if _, err := prepareBody([]byte("not json"), "m", false, formatOpenAI); err == nil {
		t.Fatalf("invalid JSON must be rejected")
	}
}

func TestEstimateTokens(t *testing.T) {
	payload := []byte(`{"model":"m","system":"You are terse.","messages":[{"role":"user","content":"Hello there, how are you doing today?"}]}`)
	count := estimateTokens(payload)
	if count < 10 || count > 40 {
		t.Fatalf("estimate out of range: %d", count)
	}
	if estimateTokens([]byte("{")) != 0 {
		t.Fatalf("invalid JSON should estimate zero")
	}
}

func TestOutputChunksPerFormat(t *testing.T) {
	chatFrame := []byte("data: {\"id\":\"x\",\"choices\":[]}\n\n")
	chunks := outputChunks(formatOpenAI, chatFrame)
	if len(chunks) != 1 || string(chunks[0]) != `{"id":"x","choices":[]}` {
		t.Fatalf("chat chunks must be bare JSON, got %q", chunks)
	}
	if got := outputChunks(formatOpenAI, []byte("data: [DONE]\n\n")); got != nil {
		t.Fatalf("[DONE] must not be forwarded for chat completions, got %q", got)
	}
	if got := outputChunks(formatOpenAI, []byte(": keep-alive\n\n")); got != nil {
		t.Fatalf("comments must be dropped, got %q", got)
	}
	claudeFrame := []byte("event: message_start\ndata: {\"type\":\"message_start\"}\n\n")
	if got := outputChunks(formatClaude, claudeFrame); len(got) != 1 || string(got[0]) != string(claudeFrame) {
		t.Fatalf("claude frames must pass through verbatim, got %q", got)
	}
	responsesFrame := []byte("event: response.created\ndata: {\"type\":\"response.created\"}\n\n")
	if got := outputChunks(formatOpenAIResponses, responsesFrame); len(got) != 1 || string(got[0]) != string(responsesFrame) {
		t.Fatalf("responses frames must pass through verbatim, got %q", got)
	}
	multi := sseDataPayload([]byte("data: {\"a\":\r\ndata: 1}\r\n\r\n"))
	if string(multi) != "{\"a\":\n1}" {
		t.Fatalf("multi-line data must be joined with newlines, got %q", multi)
	}
}

func TestBaseModelName(t *testing.T) {
	if got := baseModelName(" deepseek-v4.1-flash:1017(medium) "); got != "deepseek-v4.1-flash:1017" {
		t.Fatalf("baseModelName = %q", got)
	}
	if got := baseModelName("glm-5.3"); got != "glm-5.3" {
		t.Fatalf("baseModelName = %q", got)
	}
}
