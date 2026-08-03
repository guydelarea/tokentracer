package openai

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestParseCodexRequest(t *testing.T) {
	body := []byte(`{
	  "model":"gpt-5.6-sol",
	  "stream":true,
	  "prompt_cache_key":"019fa00f-68e9-7553-84dc-da2b03b7521c",
	  "client_metadata":{"parent_session_id":"parent-thread"},
	  "input":[
	    {"type":"additional_tools","role":"developer","tools":[
	      {"type":"function","name":"search","description":"find code"},
	      {"type":"function","name":"spawn_agent","description":"delegate"}
	    ]},
	    {"type":"message","role":"developer","content":[{"type":"input_text","text":"repo rules"}]},
	    {"type":"message","role":"user","content":[{"type":"input_text","text":"environment envelope"}]},
	    {"type":"message","role":"user","content":[{"type":"input_text","text":"Trace this request"}]}
	  ]
	}`)

	facts, err := ParseRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	if facts.Model != "gpt-5.6-sol" || !facts.Stream {
		t.Fatalf("model/stream = %q/%v", facts.Model, facts.Stream)
	}
	if facts.SessionID != "019fa00f-68e9-7553-84dc-da2b03b7521c" {
		t.Errorf("SessionID = %q", facts.SessionID)
	}
	if facts.ParentSessionID != "parent-thread" {
		t.Errorf("ParentSessionID = %q", facts.ParentSessionID)
	}
	if facts.ToolCount != 2 || facts.ToolsBytes == 0 {
		t.Errorf("tools = %d/%d bytes", facts.ToolCount, facts.ToolsBytes)
	}
	if facts.Turns != 2 {
		t.Errorf("Turns = %d, want 2 user items", facts.Turns)
	}
	if facts.Label != "Trace this request" || facts.FirstText != "Trace this request" {
		t.Errorf("label/text = %q/%q", facts.Label, facts.FirstText)
	}
	if facts.SystemBytes == 0 || facts.MessagesBytes == 0 || facts.TotalBytes != len(body) {
		t.Errorf("byte split = %+v", facts)
	}
}

func TestParseOpenCodeRequestAndBreakdown(t *testing.T) {
	body := []byte(`{
	  "model":"gpt-5.6-terra",
	  "instructions":"You are OpenCode.",
	  "prompt_cache_key":"ses_123",
	  "max_output_tokens":4096,
	  "reasoning":{"effort":"high"},
	  "text":{"verbosity":"low"},
	  "tools":[
	    {"type":"function","name":"bash","description":"run commands"},
	    {"type":"function","name":"read","description":"read files and return text"}
	  ],
	  "input":[
	    {"role":"user","content":[{"type":"input_text","text":"generated envelope"}]},
	    {"role":"user","content":[{"type":"input_text","text":"Fix the parser"}]}
	  ]
	}`)

	facts, err := ParseRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	if facts.SessionID != "ses_123" || facts.MaxTokens != 4096 {
		t.Errorf("session/max = %q/%d", facts.SessionID, facts.MaxTokens)
	}
	if facts.Label != "Fix the parser" {
		t.Errorf("Label = %q", facts.Label)
	}

	bd, err := BreakdownRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(bd.Tools) != 2 || bd.Tools[0].Name != "read" {
		t.Errorf("tools not sorted largest-first: %+v", bd.Tools)
	}
	if len(bd.System) != 1 || len(bd.Messages) != 2 {
		t.Errorf("breakdown = %+v", bd)
	}
	if !bd.Flags.Thinking || !bd.Flags.OutputConfig {
		t.Errorf("flags = %+v", bd.Flags)
	}
	var toolBytes, systemBytes, messageBytes int
	for _, item := range bd.Tools {
		toolBytes += item.Bytes
	}
	for _, item := range bd.System {
		systemBytes += item.Bytes
	}
	for _, item := range bd.Messages {
		messageBytes += item.Bytes
	}
	if toolBytes != facts.ToolsBytes || systemBytes != facts.SystemBytes || messageBytes != facts.MessagesBytes {
		t.Errorf("breakdown sums %d/%d/%d != facts %d/%d/%d",
			toolBytes, systemBytes, messageBytes, facts.ToolsBytes, facts.SystemBytes, facts.MessagesBytes)
	}
}

func TestPrefixHashesNameToolsSystemAndMessages(t *testing.T) {
	base := []byte(`{"model":"m","instructions":"system","tools":[{"type":"function","name":"a"}],"input":[{"role":"user","content":"hello"}]}`)
	toolChanged := []byte(`{"model":"m","instructions":"system","tools":[{"type":"function","name":"b"}],"input":[{"role":"user","content":"hello"}]}`)
	systemChanged := []byte(`{"model":"m","instructions":"other","tools":[{"type":"function","name":"a"}],"input":[{"role":"user","content":"hello"}]}`)
	messageChanged := []byte(`{"model":"m","instructions":"system","tools":[{"type":"function","name":"a"}],"input":[{"role":"user","content":"goodbye"}]}`)

	a := PrefixHashes(base)
	if len(a) != 3 {
		t.Fatalf("hash chain length = %d", len(a))
	}
	if firstDiff(a, PrefixHashes(toolChanged)) != 0 {
		t.Error("tool change did not diverge at segment 0")
	}
	if firstDiff(a, PrefixHashes(systemChanged)) != 1 {
		t.Error("system change did not diverge at segment 1")
	}
	if firstDiff(a, PrefixHashes(messageChanged)) != 2 {
		t.Error("message change did not diverge at segment 2")
	}
}

func TestDecodeSSECompletedResponse(t *testing.T) {
	body := []byte(strings.Join([]string{
		`event: response.created`,
		`data: {"type":"response.created","response":{"id":"resp_1","object":"response","status":"in_progress","model":"gpt-5.6-sol","output":[]}}`,
		``,
		`event: response.output_item.done`,
		`data: {"type":"response.output_item.done","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"bash","arguments":"{\"command\":\"go test ./...\"}"}}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.6-sol-2026-07-01","future_field":{"kept":true},"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Running tests"}]},{"type":"function_call","id":"fc_1","call_id":"call_1","name":"bash","arguments":"{\"command\":\"go test ./...\"}"}],"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":60,"cache_write_tokens":20},"output_tokens":30,"output_tokens_details":{"reasoning_tokens":10}}}}`,
		``,
	}, "\n"))

	resp, facts, raw, err := DecodeSSE(body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "resp_1" || facts.Model != "gpt-5.6-sol-2026-07-01" {
		t.Fatalf("response/model = %q/%q", resp.ID, facts.Model)
	}
	if facts.Input != 20 || facts.CacheRead != 60 || facts.CacheWrite != 20 || facts.Output != 30 {
		t.Errorf("usage = %+v", facts)
	}
	if facts.Think != 10 || facts.Text+facts.Tool+facts.Think != facts.Output {
		t.Errorf("output split = think %d text %d tool %d of %d", facts.Think, facts.Text, facts.Tool, facts.Output)
	}
	if facts.Op != "tool_use · bash — go test ./..." {
		t.Errorf("Op = %q", facts.Op)
	}
	if !bytes.Contains(raw, []byte(`"future_field":{"kept":true}`)) {
		t.Errorf("assembled response dropped unknown fields: %s", raw)
	}
}

func TestDecodeIncompleteAndStreamError(t *testing.T) {
	incomplete := []byte(`{
	  "id":"resp_2","object":"response","status":"incomplete","model":"gpt-5.6-luna",
	  "incomplete_details":{"reason":"max_output_tokens"},"output":[],
	  "usage":{"input_tokens":2,"output_tokens":3}
	}`)
	_, facts, err := DecodeJSON(incomplete)
	if err != nil {
		t.Fatal(err)
	}
	if facts.StopReason != "max_tokens" {
		t.Errorf("StopReason = %q", facts.StopReason)
	}

	stream := []byte("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"server_error\",\"message\":\"try again\"}}\n\n")
	_, facts, _, err = DecodeSSE(stream)
	if err != nil {
		t.Fatal(err)
	}
	if facts.ErrType != "server_error" || facts.ErrMsg != "try again" {
		t.Errorf("stream error = %+v", facts)
	}
}

func TestToolResultsAndCalls(t *testing.T) {
	body := []byte(`{
	  "model":"m","prompt_cache_key":"ses_1",
	  "input":[
	    {"role":"user","content":"do it"},
	    {"type":"function_call","id":"fc_old","call_id":"call_old","name":"read","arguments":"{\"path\":\"old\"}"},
	    {"type":"function_call_output","call_id":"call_old","output":"old result"},
	    {"type":"function_call","id":"fc_1","call_id":"call_1","name":"bash","arguments":"{\"command\":\"go test\"}"},
	    {"type":"function_call_output","call_id":"call_1","output":"ok"}
	  ]
	}`)
	results := ResultsInContext(body)
	if len(results) != 2 {
		t.Fatalf("ResultsInContext = %+v", results)
	}
	current := ToolResults(body)
	if len(current) != 1 || current[0].ToolUseID != "call_1" {
		t.Errorf("ToolResults = %+v", current)
	}

	response := []byte(`{"object":"response","output":[
	  {"type":"function_call","id":"fc_1","call_id":"call_1","name":"bash","arguments":"{\"command\":\"go test\"}"},
	  {"type":"message","content":[{"type":"output_text","text":"done"}]}
	]}`)
	calls := ResponseCalls(response)
	if len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Name != "bash" {
		t.Errorf("ResponseCalls = %+v", calls)
	}
	var args map[string]string
	if json.Unmarshal(calls[0].Arguments, &args) != nil || args["command"] != "go test" {
		t.Errorf("call arguments = %s", calls[0].Arguments)
	}
}

func TestAgentPrompts(t *testing.T) {
	items := []OutputItem{
		{Type: "function_call", CallID: "c1", Name: "spawn_agent", Arguments: json.RawMessage(`"{\"message\":\"inspect billing\"}"`)},
		{Type: "function_call", CallID: "c2", Name: "bash", Arguments: json.RawMessage(`"{\"command\":\"true\"}"`)},
	}
	got := AgentPrompts(items)
	if len(got) != 1 || got[0] != "inspect billing" {
		t.Errorf("AgentPrompts = %#v", got)
	}
}

func TestDecodeError(t *testing.T) {
	typ, msg := DecodeError(429, []byte(`{"error":{"type":"rate_limit_error","code":"rate_limit","message":"slow down"}}`))
	if typ != "rate_limit_error" || msg != "slow down" {
		t.Errorf("DecodeError = %q/%q", typ, msg)
	}
	typ, msg = DecodeError(503, []byte(`not json`))
	if typ != "http_503" || msg == "" {
		t.Errorf("fallback DecodeError = %q/%q", typ, msg)
	}
}

func TestMalformedBodies(t *testing.T) {
	if _, err := ParseRequest([]byte(`{"input":`)); err == nil {
		t.Error("malformed request accepted")
	}
	if _, _, err := DecodeJSON([]byte(`{}`)); err == nil {
		t.Error("empty response accepted")
	}
	if _, _, _, err := DecodeSSE([]byte("data: nope\n")); err == nil {
		t.Error("stream without a response accepted")
	}
}

func firstDiff(a, b []string) int {
	for i := 0; i < min(len(a), len(b)); i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return -1
}
