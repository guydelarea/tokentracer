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
		{http.MethodPost, "/chat/completions", true},
		{http.MethodPost, "/v1/chat/completions", true},
		{http.MethodPost, "/openai/deployments/coder/chat/completions", true},
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

func TestObserveOpenAIChatCompletions(t *testing.T) {
	request := []byte(`{
	  "model":"qwen3-coder","stream":true,"max_completion_tokens":1000,"user":"opencode-session",
	  "tools":[{"type":"function","function":{"name":"bash","description":"run a command"}}],
	  "messages":[
	    {"role":"developer","content":"Use tools when needed."},
	    {"role":"user","content":"Run the tests"}
	  ]
	}`)
	response := []byte("data: " +
		`{"id":"chat_1","object":"chat.completion.chunk","model":"qwen3-coder","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"go test"}}]}}]}` + "\n\n" +
		"data: " +
		`{"id":"chat_1","object":"chat.completion.chunk","model":"qwen3-coder","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":" ./...\"}"}}]},"finish_reason":"tool_calls"}]}` + "\n\n" +
		"data: " +
		`{"id":"chat_1","object":"chat.completion.chunk","model":"qwen3-coder","choices":[],"usage":{"prompt_tokens":80,"prompt_tokens_details":{"cached_tokens":20},"completion_tokens":30,"completion_tokens_details":{"reasoning_tokens":5}}}` + "\n\n" +
		"data: [DONE]\n\n")

	requestObs := ObserveRequest("/v1/chat/completions", request)
	responseObs := ObserveResponse("/v1/chat/completions", 200, true, response)
	if requestObs.RequestErr != nil || responseObs.ResponseErr != nil || responseObs.Problem != nil {
		t.Fatalf("Observe errors: request=%v response=%v problem=%+v", requestObs.RequestErr, responseObs.ResponseErr, responseObs.Problem)
	}
	if requestObs.Kind != OpenAIChat || requestObs.Request.SessionID != "opencode-session" || requestObs.Request.ToolCount != 1 {
		t.Errorf("request observation = %+v", requestObs)
	}
	if requestObs.Request.SystemBytes == 0 || requestObs.Request.MessagesBytes == 0 {
		t.Errorf("request byte split = %+v", requestObs.Request)
	}
	if responseObs.Response.Input != 60 || responseObs.Response.CacheRead != 20 || responseObs.Response.Output != 30 || responseObs.Response.Think != 5 {
		t.Errorf("response usage = %+v", responseObs.Response)
	}
	if responseObs.Response.Op != "tool_use · bash — go test ./..." {
		t.Errorf("response op = %q", responseObs.Response.Op)
	}
	if !bytes.Contains(responseObs.ResponseCapture, []byte(`go test ./...`)) {
		t.Errorf("assembled capture did not merge tool arguments: %s", responseObs.ResponseCapture)
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

	chatRequest := []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}]}`)
	chat, err := InspectRequestAt("/chat/completions", chatRequest)
	if err != nil {
		t.Fatal(err)
	}
	if chat.Kind != OpenAIChat || chat.LatestUserText != "hello" {
		t.Errorf("chat inspection = %+v", chat)
	}

	openAIResponse := []byte(`{"object":"response","output":[{"type":"function_call","call_id":"c1","name":"bash","arguments":"{\"command\":\"true\"}"}]}`)
	claudeResponse := []byte(`{"type":"message","content":[{"type":"tool_use","id":"c2","name":"Bash","input":{"command":"true"}}]}`)
	chatResponse := []byte(`{"object":"chat.completion","choices":[{"message":{"role":"assistant","tool_calls":[{"id":"c3","type":"function","function":{"name":"bash","arguments":"{\"command\":\"true\"}"}}]}}]}`)
	for name, body := range map[string][]byte{"openai": openAIResponse, "anthropic": claudeResponse, "chat": chatResponse} {
		calls := ResponseCalls(body)
		if len(calls) != 1 || calls[0].ID == "" {
			t.Errorf("%s calls = %+v", name, calls)
		}
	}
}
