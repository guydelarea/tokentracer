package wire

import (
	"bytes"
	"net/http"
	"testing"
)

func TestRecordable(t *testing.T) {
	tests := []struct {
		method string
		path   string
		want   bool
	}{
		{http.MethodPost, "/v1/messages", true},
		{http.MethodPost, "/v1/projects/p/locations/us/publishers/anthropic/models/claude-sonnet-5:streamRawPredict", true},
		{http.MethodPost, "/responses", true},
		{http.MethodPost, "/v1/responses", true},
		{http.MethodPost, "/backend-api/codex/responses", true},
		{http.MethodPost, "/responses/compact", false},
		{http.MethodPost, "/v1/messages/count_tokens", false},
		{http.MethodGet, "/responses", false},
	}
	for _, tt := range tests {
		if got := Recordable(tt.method, tt.path); got != tt.want {
			t.Errorf("Recordable(%q, %q) = %v, want %v", tt.method, tt.path, got, tt.want)
		}
	}
}

func TestObserveOpenAIResponses(t *testing.T) {
	request := []byte(`{
	  "model":"gpt-5.6-sol","stream":true,"prompt_cache_key":"thread-1",
	  "tools":[{"type":"function","name":"exec_command"}],
	  "input":[{"role":"user","content":[{"type":"input_text","text":"Run the tests"}]}]
	}`)
	response := []byte("event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.6-sol","output":[{"type":"function_call","call_id":"call_1","name":"exec_command","arguments":"{\"cmd\":\"go test ./...\"}"}],"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":60,"cache_write_tokens":20},"output_tokens":30,"output_tokens_details":{"reasoning_tokens":10}}}}` +
		"\n\n")

	requestObs := ObserveRequest("/responses", request)
	responseObs := ObserveResponse("/responses", 200, true, response)
	if requestObs.RequestErr != nil || responseObs.ResponseErr != nil || responseObs.Problem != nil {
		t.Fatalf("Observe errors: request=%v response=%v problem=%+v",
			requestObs.RequestErr, responseObs.ResponseErr, responseObs.Problem)
	}
	if requestObs.Kind != OpenAI || requestObs.Request.SessionID != "thread-1" || requestObs.Request.ToolCount != 1 {
		t.Errorf("request observation = %+v", requestObs)
	}
	if responseObs.Response.Input != 20 || responseObs.Response.CacheRead != 60 ||
		responseObs.Response.CacheW5m != 20 || responseObs.Response.Output != 30 {
		t.Errorf("response usage = %+v", responseObs.Response)
	}
	if responseObs.Response.Think != 10 || responseObs.Response.Op != "tool_use · exec_command — go test ./..." {
		t.Errorf("response shape = %+v", responseObs.Response)
	}
	if !bytes.Contains(responseObs.ResponseCapture, []byte(`"id":"resp_1"`)) {
		t.Errorf("assembled capture = %s", responseObs.ResponseCapture)
	}
}

func TestInspectAndResponseCallsRouteBothDialects(t *testing.T) {
	openAIRequest := []byte(`{"model":"gpt-5.6","input":[{"role":"user","content":"hello"}]}`)
	openAI, err := InspectRequest(openAIRequest)
	if err != nil {
		t.Fatal(err)
	}
	if openAI.Kind != OpenAI || openAI.LatestUserText != "hello" {
		t.Errorf("OpenAI inspection = %+v", openAI)
	}

	anthropicRequest := []byte(`{"model":"claude-sonnet-5","max_tokens":10,"messages":[{"role":"user","content":"hello"}]}`)
	claude, err := InspectRequest(anthropicRequest)
	if err != nil {
		t.Fatal(err)
	}
	if claude.Kind != Anthropic || claude.LatestUserText != "hello" {
		t.Errorf("Anthropic inspection = %+v", claude)
	}

	openAIResponse := []byte(`{"object":"response","output":[{"type":"function_call","call_id":"c1","name":"bash","arguments":"{\"command\":\"true\"}"}]}`)
	claudeResponse := []byte(`{"type":"message","content":[{"type":"tool_use","id":"c2","name":"Bash","input":{"command":"true"}}]}`)
	for name, body := range map[string][]byte{"openai": openAIResponse, "anthropic": claudeResponse} {
		calls := ResponseCalls(body)
		if len(calls) != 1 || calls[0].ID == "" {
			t.Errorf("%s calls = %+v", name, calls)
		}
	}
}
