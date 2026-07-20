package anthropic

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// fixtureRequest is the verbatim request body of a real Claude Code turn:
// claude-sonnet-5, 119 tools, 3 system blocks, 7 messages, session id present.
func fixtureRequest(t *testing.T) []byte {
	t.Helper()
	f, err := os.Open("../../testdata/anthropic_capture.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var fix struct {
		Request json.RawMessage `json:"request"`
	}
	if err := json.NewDecoder(zr).Decode(&fix); err != nil {
		t.Fatal(err)
	}
	return fix.Request
}

func replaySSE(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../../testdata/replay.sse")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The extraction table from the spec, pinned against the real capture.
func TestParseRequestFixture(t *testing.T) {
	got, err := ParseRequest(fixtureRequest(t))
	if err != nil {
		t.Fatalf("ParseRequest: %v", err)
	}

	want := RequestFacts{
		Model:     "claude-sonnet-5",
		SessionID: "210b4cd3-be09-484a-a703-d00f5c6e855f",
		Stream:    true,
		Turns:     7,
		ToolCount: 119,
		MaxTokens: 64000, // a real request leaves room for an answer; a probe asks for 1

		TotalBytes:    308535,
		ToolsBytes:    232586,
		SystemBytes:   29910,
		MessagesBytes: 45445,

		// Not "<system-reminder>…": the human's actual words, which sit in the
		// SECOND text block of the first user message.
		Label:     "hello world!",
		FirstText: "hello world!",
	}
	if got != want {
		t.Errorf("ParseRequest facts mismatch\n got: %+v\nwant: %+v", got, want)
	}

	// The headline finding this product exists to show: tool schemas are three
	// quarters of the wire.
	if pct := 100 * float64(got.ToolsBytes) / float64(got.TotalBytes); pct < 74 || pct > 77 {
		t.Errorf("tools share = %.1f%%, want ~75%%", pct)
	}
}

// Message content arrives as a raw string AND as a block array, in the same
// conversation. Roles include "system" mid-conversation. Neither may be assumed.
func TestParseRequestContentForms(t *testing.T) {
	body := []byte(`{
	  "model": "claude-sonnet-5",
	  "messages": [
	    {"role": "system", "content": "a system-reminder message, not the user"},
	    {"role": "user", "content": [
	      {"type": "text", "text": "<system-reminder>\nignore me\n</system-reminder>"},
	      {"type": "text", "text": "the real question"}
	    ]},
	    {"role": "assistant", "content": "plain string content"}
	  ]
	}`)

	got, err := ParseRequest(body)
	if err != nil {
		t.Fatalf("ParseRequest: %v", err)
	}
	if got.Label != "the real question" {
		t.Errorf("Label = %q, want the first non-reminder user text", got.Label)
	}
	if got.Turns != 3 {
		t.Errorf("Turns = %d, want 3 (system-role messages are still turns)", got.Turns)
	}
}

func TestParseRequestLabelEdgeCases(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"no user message", `{"model":"m","messages":[{"role":"assistant","content":"hi"}]}`, ""},
		{"no messages at all", `{"model":"m"}`, ""},
		{"user string content", `{"model":"m","messages":[{"role":"user","content":"just a string"}]}`, "just a string"},
		{"whitespace collapsed", `{"model":"m","messages":[{"role":"user","content":"two\n\nlines   here"}]}`, "two lines here"},
		{
			"truncated to 64 chars",
			`{"model":"m","messages":[{"role":"user","content":"` + strings.Repeat("x", 100) + `"}]}`,
			strings.Repeat("x", 64),
		},
		{
			"user message that is only a reminder falls through to the next",
			`{"model":"m","messages":[
			  {"role":"user","content":"<system-reminder>noise</system-reminder>"},
			  {"role":"user","content":"the human"}
			]}`,
			"the human",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseRequest([]byte(tc.body))
			if err != nil {
				t.Fatalf("ParseRequest: %v", err)
			}
			if got.Label != tc.want {
				t.Errorf("Label = %q, want %q", got.Label, tc.want)
			}
		})
	}
}

func TestParseRequestRejectsGarbage(t *testing.T) {
	for _, body := range []string{`not json at all`, ``} {
		if _, err := ParseRequest([]byte(body)); err == nil {
			t.Errorf("ParseRequest(%q) = nil error, want an error so the Recorder can degrade", body)
		}
	}
}

// A Vertex body names no model — that is the URL's job, not a parse failure.
func TestParseRequestToleratesMissingModel(t *testing.T) {
	got, err := ParseRequest([]byte(`{"anthropic_version":"vertex-2023-10-16","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("ParseRequest: %v", err)
	}
	if got.Model != "" || got.Turns != 1 {
		t.Errorf("Model = %q, Turns = %d; want \"\" and 1", got.Model, got.Turns)
	}
}

func TestVertexModel(t *testing.T) {
	cases := map[string]string{
		"/v1/projects/p/locations/us-east5/publishers/anthropic/models/claude-sonnet-5:streamRawPredict": "claude-sonnet-5",
		"/projects/p/locations/global/publishers/anthropic/models/claude-opus-4-5@20251101:rawPredict":   "claude-opus-4-5@20251101",
		"/v1/projects/p/locations/global/publishers/anthropic/models/count-tokens:rawPredict":            "count-tokens",
		"/v1/messages":              "",
		"/v1/messages/count_tokens": "",
	}
	for path, want := range cases {
		if got := VertexModel(path); got != want {
			t.Errorf("VertexModel(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestBreakdownRequestFixture(t *testing.T) {
	b, err := BreakdownRequest(fixtureRequest(t))
	if err != nil {
		t.Fatalf("BreakdownRequest: %v", err)
	}

	// Tools: 119 of them, sorted biggest-first, wildly skewed.
	if len(b.Tools) != 119 {
		t.Fatalf("len(Tools) = %d, want 119", len(b.Tools))
	}
	if b.Tools[0].Name != "Workflow" || b.Tools[0].Bytes != 21534 {
		t.Errorf("largest tool = %+v, want Workflow at 21534 bytes", b.Tools[0])
	}
	last := b.Tools[len(b.Tools)-1]
	if last.Name != "mcp__codebase-memory-mcp__list_projects" || last.Bytes != 141 {
		t.Errorf("smallest tool = %+v, want list_projects at 141 bytes", last)
	}
	for i := 1; i < len(b.Tools); i++ {
		if b.Tools[i-1].Bytes < b.Tools[i].Bytes {
			t.Fatalf("tools not sorted desc at %d: %d < %d", i, b.Tools[i-1].Bytes, b.Tools[i].Bytes)
		}
	}

	// System: 3 blocks, the last two cached for an hour.
	wantSystem := []SystemItem{
		{Bytes: 95, CacheControl: ""},
		{Bytes: 130, CacheControl: "ephemeral/1h"},
		{Bytes: 29685, CacheControl: "ephemeral/1h"},
	}
	if len(b.System) != len(wantSystem) {
		t.Fatalf("len(System) = %d, want %d", len(b.System), len(wantSystem))
	}
	for i, w := range wantSystem {
		if b.System[i] != w {
			t.Errorf("System[%d] = %+v, want %+v", i, b.System[i], w)
		}
	}

	// Messages: the exact role/shape mix the real data dictates.
	wantMsgs := []MessageItem{
		{Role: "user", Bytes: 14146, BlockKinds: []string{"text", "text"}},
		{Role: "system", Bytes: 21503, BlockKinds: []string{"text"}}, // string content
		{Role: "assistant", Bytes: 1965, BlockKinds: []string{"thinking", "text"}},
		{Role: "user", Bytes: 263, BlockKinds: []string{"text"}}, // string content
		{Role: "system", Bytes: 308, BlockKinds: []string{"text"}},
		{Role: "assistant", Bytes: 6666, BlockKinds: []string{"thinking", "text", "tool_use", "tool_use"}},
		{Role: "user", Bytes: 594, BlockKinds: []string{"tool_result", "tool_result"}},
	}
	if len(b.Messages) != len(wantMsgs) {
		t.Fatalf("len(Messages) = %d, want %d", len(b.Messages), len(wantMsgs))
	}
	for i, w := range wantMsgs {
		got := b.Messages[i]
		if got.Role != w.Role || got.Bytes != w.Bytes || strings.Join(got.BlockKinds, ",") != strings.Join(w.BlockKinds, ",") {
			t.Errorf("Messages[%d] = %+v, want %+v", i, got, w)
		}
	}

	if want := (Flags{Thinking: true, ContextManagement: true, OutputConfig: true}); b.Flags != want {
		t.Errorf("Flags = %+v, want %+v", b.Flags, want)
	}
}

// The invariant that lets the inspector be trusted: the breakdown is an
// itemization of the same bytes the fact row counted, not a second opinion.
func TestBreakdownSumsEqualFactColumns(t *testing.T) {
	body := fixtureRequest(t)

	facts, err := ParseRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	b, err := BreakdownRequest(body)
	if err != nil {
		t.Fatal(err)
	}

	var tools, system, messages int
	for _, x := range b.Tools {
		tools += x.Bytes
	}
	for _, x := range b.System {
		system += x.Bytes
	}
	for _, x := range b.Messages {
		messages += x.Bytes
	}

	if tools != facts.ToolsBytes {
		t.Errorf("Σ tool bytes = %d, fact column = %d", tools, facts.ToolsBytes)
	}
	if system != facts.SystemBytes {
		t.Errorf("Σ system bytes = %d, fact column = %d", system, facts.SystemBytes)
	}
	if messages != facts.MessagesBytes {
		t.Errorf("Σ message bytes = %d, fact column = %d", messages, facts.MessagesBytes)
	}
}

func TestDecodeSSEReplay(t *testing.T) {
	r, err := DecodeSSE(replaySSE(t))
	if err != nil {
		t.Fatalf("DecodeSSE: %v", err)
	}

	if r.Model != "claude-sonnet-5" {
		t.Errorf("Model = %q", r.Model)
	}
	if r.StopReason != "tool_use" {
		t.Errorf("StopReason = %q", r.StopReason)
	}

	// Usage merges later-wins per key: message_start seeded output_tokens=1 and
	// message_delta overwrote it with the real 213 — while the input-side keys it
	// never mentioned survive untouched.
	want := Usage{In: 1234, Out: 213, CacheRead: 71234, W5m: 2048, W1h: 29105}
	if r.Usage != want {
		t.Errorf("Usage = %+v, want %+v", r.Usage, want)
	}

	if len(r.Content) != 3 {
		t.Fatalf("len(Content) = %d, want 3 (thinking, text, tool_use)", len(r.Content))
	}

	// The lossiness lesson: thinking text and signature are kept, not just the type.
	think := r.Content[0]
	if think.Type != "thinking" {
		t.Fatalf("Content[0].Type = %q", think.Type)
	}
	if !strings.HasPrefix(think.Thinking, "The user wants me to list the files") || !strings.HasSuffix(think.Thinking, "the project id.") {
		t.Errorf("thinking text was not fully assembled from its deltas: %q", think.Thinking)
	}
	if think.Signature == "" {
		t.Error("thinking signature was dropped")
	}

	if txt := r.Content[1]; txt.Type != "text" || txt.Text != "Let me look at the design project's files." {
		t.Errorf("Content[1] = %+v, want the assembled text block", txt)
	}

	// tool_use input arrives as input_json_delta fragments and must accumulate
	// back into real JSON — tokentrace stored it as a string and lost it.
	tu := r.Content[2]
	if tu.Type != "tool_use" || tu.Name != "DesignSync" || tu.ID != "toolu_01C6xGFu6hrXh3ot89q8xKJP" {
		t.Errorf("Content[2] = %+v, want the DesignSync tool_use", tu)
	}
	var input struct {
		Method    string `json:"method"`
		ProjectID string `json:"projectId"`
	}
	if err := json.Unmarshal(tu.Input, &input); err != nil {
		t.Fatalf("tool input is not valid JSON: %v (%q)", err, tu.Input)
	}
	if input.Method != "list_files" || input.ProjectID != "72e7ae30-dd29-4621-84fd-bc17d419f02f" {
		t.Errorf("tool input = %+v, want the fixture's values", input)
	}

	if got := r.Op(); got != "tool_use · DesignSync" {
		t.Errorf("Op() = %q, want %q", got, "tool_use · DesignSync")
	}
}

func TestOp(t *testing.T) {
	tests := []struct {
		name string
		resp Response
		want string
	}{
		{
			"tool with a command gets a snippet",
			Response{StopReason: "tool_use", Content: []Block{
				{Type: "tool_use", Name: "Bash", Input: json.RawMessage(`{"command":"git status --short --branch"}`)},
			}},
			"tool_use · Bash — git status --short --bra…",
		},
		{
			"tool without a command is just the name",
			Response{StopReason: "tool_use", Content: []Block{
				{Type: "tool_use", Name: "DesignSync", Input: json.RawMessage(`{"method":"list_files"}`)},
			}},
			"tool_use · DesignSync",
		},
		{
			"no tool falls back to the stop reason",
			Response{StopReason: "end_turn", Content: []Block{{Type: "text", Text: "done"}}},
			"end_turn",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.resp.Op(); got != tc.want {
				t.Errorf("Op() = %q, want %q", got, tc.want)
			}
		})
	}
}

// Adversarial inputs. The rule for all of them: decode keeps what it understood
// and never fails the whole stream over one bad event.
func TestDecodeSSEAdversarial(t *testing.T) {
	t.Run("truncated mid-event", func(t *testing.T) {
		full := replaySSE(t)
		cut := full[:len(full)*2/3] // slice through the middle of an event

		r, err := DecodeSSE(cut)
		if err != nil {
			t.Fatalf("a truncated stream must still decode what arrived: %v", err)
		}
		if r.Model != "claude-sonnet-5" {
			t.Errorf("Model lost: %q", r.Model)
		}
		if r.Usage.In != 1234 {
			t.Errorf("input usage lost: %+v", r.Usage)
		}
		if len(r.Content) == 0 {
			t.Error("every block was thrown away")
		}
	})

	t.Run("error event after partial content", func(t *testing.T) {
		stream := `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-5","role":"assistant","usage":{"input_tokens":10}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial answer"}}

event: error
data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}

`
		r, err := DecodeSSE([]byte(stream))
		if err != nil {
			t.Fatalf("DecodeSSE: %v", err)
		}
		if r.ErrType != "overloaded_error" || r.ErrMsg != "Overloaded" {
			t.Errorf("error not captured: %q / %q", r.ErrType, r.ErrMsg)
		}
		if len(r.Content) != 1 || r.Content[0].Text != "partial answer" {
			t.Errorf("content generated before the error was lost: %+v", r.Content)
		}
		if r.Usage.In != 10 {
			t.Errorf("usage before the error was lost: %+v", r.Usage)
		}
	})

	t.Run("unknown event type and unknown block type", func(t *testing.T) {
		stream := `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-5","usage":{"input_tokens":7}}}

event: some_future_event
data: {"type":"some_future_event","payload":{"anything":true}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search"}}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"still here"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"future_delta","whatever":"x"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":42}}

`
		r, err := DecodeSSE([]byte(stream))
		if err != nil {
			t.Fatalf("an unknown event must not fail the decode: %v", err)
		}
		if r.StopReason != "end_turn" || r.Usage.Out != 42 || r.Usage.In != 7 {
			t.Errorf("known facts lost around the unknown events: stop=%q usage=%+v", r.StopReason, r.Usage)
		}
		if len(r.Content) != 2 {
			t.Fatalf("len(Content) = %d, want 2 (the unknown block is recorded, not dropped)", len(r.Content))
		}
		if r.Content[0].Type != "server_tool_use" {
			t.Errorf("unknown block type not preserved: %+v", r.Content[0])
		}
		if r.Content[1].Text != "still here" {
			t.Errorf("known block damaged by the unknown delta: %+v", r.Content[1])
		}
	})

	t.Run("tool input cut off mid-argument is kept as evidence", func(t *testing.T) {
		stream := `event: message_start
data: {"type":"message_start","message":{"id":"m","model":"claude-sonnet-5"}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"Bash","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\": \"rm -"}}

`
		r, err := DecodeSSE([]byte(stream))
		if err != nil {
			t.Fatalf("DecodeSSE: %v", err)
		}
		if len(r.Content) != 1 {
			t.Fatalf("len(Content) = %d, want 1", len(r.Content))
		}
		if !json.Valid(r.Content[0].Input) {
			t.Errorf("Input must remain valid JSON even when the fragment is not: %q", r.Content[0].Input)
		}
		if !strings.Contains(string(r.Content[0].Input), "rm -") {
			t.Errorf("the partial fragment was dropped instead of kept: %q", r.Content[0].Input)
		}
	})

	t.Run("garbage is an error so the Recorder can degrade", func(t *testing.T) {
		if _, err := DecodeSSE([]byte("this is not an SSE stream at all")); err == nil {
			t.Error("DecodeSSE(garbage) = nil error, want an error")
		}
	})
}

// AgentPrompts is the parent half of the subagent link: the prompts a reply's
// Task/Agent tool_use blocks carried, and nothing else — a Bash command or an
// MCP tool that happens to take a "prompt" argument must not register a spawn.
func TestAgentPrompts(t *testing.T) {
	blocks := []Block{
		{Type: "tool_use", Name: "Task", Input: json.RawMessage(`{"subagent_type":"Explore","prompt":"find the flaky tests"}`)},
		{Type: "tool_use", Name: "Agent", Input: json.RawMessage(`{"prompt":"audit the proxy"}`)},
		{Type: "tool_use", Name: "Bash", Input: json.RawMessage(`{"command":"git status"}`)},
		{Type: "tool_use", Name: "mcp__gen__run", Input: json.RawMessage(`{"prompt":"not a spawn"}`)},
		{Type: "tool_use", Name: "Task", Input: json.RawMessage(`{"description":"promptless"}`)},
		{Type: "text", Text: "done"},
	}
	got := AgentPrompts(blocks)
	want := []string{"find the flaky tests", "audit the proxy"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("AgentPrompts = %q, want %q", got, want)
	}

	if p := AgentPrompts(nil); p != nil {
		t.Errorf("AgentPrompts(nil) = %q, want none", p)
	}
}

func TestDecodeJSON(t *testing.T) {
	body := []byte(`{
	  "id": "msg_01",
	  "type": "message",
	  "role": "assistant",
	  "model": "claude-haiku-4-5",
	  "stop_reason": "end_turn",
	  "content": [{"type": "text", "text": "hello"}],
	  "usage": {
	    "input_tokens": 11,
	    "output_tokens": 22,
	    "cache_read_input_tokens": 33,
	    "cache_creation": {"ephemeral_5m_input_tokens": 44, "ephemeral_1h_input_tokens": 55}
	  }
	}`)

	r, err := DecodeJSON(body)
	if err != nil {
		t.Fatalf("DecodeJSON: %v", err)
	}
	if r.Model != "claude-haiku-4-5" || r.StopReason != "end_turn" {
		t.Errorf("got %+v", r)
	}
	want := Usage{In: 11, Out: 22, CacheRead: 33, W5m: 44, W1h: 55}
	if r.Usage != want {
		t.Errorf("Usage = %+v, want %+v", r.Usage, want)
	}
	if len(r.Content) != 1 || r.Content[0].Text != "hello" {
		t.Errorf("Content = %+v", r.Content)
	}
	if _, err := DecodeJSON([]byte(`{"garbage":true}`)); err == nil {
		t.Error("DecodeJSON on a non-message = nil error, want an error")
	}
}

func TestDecodeError(t *testing.T) {
	t.Run("structured error body", func(t *testing.T) {
		typ, msg := DecodeError(429, []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Number of requests has exceeded your limit."}}`))
		if typ != "rate_limit_error" {
			t.Errorf("errType = %q", typ)
		}
		if !strings.HasPrefix(msg, "Number of requests") {
			t.Errorf("errMsg = %q", msg)
		}
	})

	t.Run("unparseable body falls back to the status", func(t *testing.T) {
		typ, msg := DecodeError(529, []byte(`<html>upstream is on fire</html>`))
		if typ != "http_529" {
			t.Errorf("errType = %q, want http_529", typ)
		}
		if !strings.Contains(msg, "on fire") {
			t.Errorf("errMsg = %q, want the body kept as evidence", msg)
		}
	})

	t.Run("empty body still yields a usable row", func(t *testing.T) {
		typ, _ := DecodeError(500, nil)
		if typ != "http_500" {
			t.Errorf("errType = %q, want http_500", typ)
		}
	})
}
