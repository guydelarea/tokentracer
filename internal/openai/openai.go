// Package openai is the vendor module for the OpenAI Responses API: pure
// functions, no I/O. It understands both the public /v1/responses spelling and
// the same wire format used by Codex and OpenCode through ChatGPT.
//
// Requests remain raw while they are measured. Streamed responses are reduced
// to the final Response object, preserving unknown fields in the capture.
package openai

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

const labelMax = 64

// RequestFacts is everything TokenTracer's fact row needs from a Responses
// request.
type RequestFacts struct {
	Model           string
	SessionID       string
	ParentSessionID string
	Stream          bool
	Turns           int
	ToolCount       int
	MaxTokens       int
	TotalBytes      int
	ToolsBytes      int
	SystemBytes     int
	MessagesBytes   int
	Label           string
	FirstText       string
}

type request struct {
	Model             string            `json:"model"`
	Stream            bool              `json:"stream"`
	MaxOutputTokens   int               `json:"max_output_tokens"`
	Instructions      json.RawMessage   `json:"instructions"`
	Input             json.RawMessage   `json:"input"`
	Tools             []json.RawMessage `json:"tools"`
	PromptCacheKey    string            `json:"prompt_cache_key"`
	User              string            `json:"user"`
	Metadata          map[string]any    `json:"metadata"`
	ClientMetadata    map[string]any    `json:"client_metadata"`
	Reasoning         json.RawMessage   `json:"reasoning"`
	ContextManagement json.RawMessage   `json:"context_management"`
	Text              json.RawMessage   `json:"text"`
}

type inputItem struct {
	Type    string            `json:"type"`
	Role    string            `json:"role"`
	Content json.RawMessage   `json:"content"`
	Tools   []json.RawMessage `json:"tools"`
	ID      string            `json:"id"`
	CallID  string            `json:"call_id"`
	Name    string            `json:"name"`
	Output  json.RawMessage   `json:"output"`
}

type contentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type tool struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

func ParseRequest(body []byte) (RequestFacts, error) {
	req, items, err := parseRequest(body)
	if err != nil {
		return RequestFacts{}, err
	}
	tools := requestTools(req, items)
	f := RequestFacts{
		Model:      req.Model,
		SessionID:  sessionID(req),
		Stream:     req.Stream,
		MaxTokens:  req.MaxOutputTokens,
		ToolCount:  len(tools),
		TotalBytes: len(body),
	}
	f.ParentSessionID = stringField(req.ClientMetadata, "parent_session_id", "parentSessionId", "parentSessionID")
	if f.ParentSessionID == "" {
		f.ParentSessionID = stringField(req.Metadata, "parent_session_id", "parentSessionId", "parentSessionID")
	}
	for _, raw := range tools {
		f.ToolsBytes += len(raw)
	}
	if present(req.Instructions) {
		f.SystemBytes += len(req.Instructions)
	}
	for _, raw := range items {
		var item inputItem
		if json.Unmarshal(raw, &item) != nil {
			f.MessagesBytes += len(raw)
			continue
		}
		switch item.Role {
		case "system", "developer":
			f.SystemBytes += len(raw)
		default:
			if item.Type != "additional_tools" {
				f.MessagesBytes += len(raw)
			}
		}
		if item.Role == "user" {
			f.Turns++
		}
	}
	f.FirstText = latestUserText(items)
	f.Label = truncate(f.FirstText, labelMax)
	return f, nil
}

func parseRequest(body []byte) (request, []json.RawMessage, error) {
	var req request
	if err := json.Unmarshal(body, &req); err != nil {
		return request{}, nil, fmt.Errorf("request json: %w", err)
	}
	if len(req.Input) == 0 {
		return request{}, nil, errors.New("request json: missing input")
	}
	items, err := inputItems(req.Input)
	if err != nil {
		return request{}, nil, err
	}
	return req, items, nil
}

func inputItems(raw json.RawMessage) ([]json.RawMessage, error) {
	if isArray(raw) {
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, fmt.Errorf("request input: %w", err)
		}
		return items, nil
	}
	// The Responses API also accepts a bare string as input.
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return nil, fmt.Errorf("request input: %w", err)
	}
	item, _ := json.Marshal(map[string]any{
		"role":    "user",
		"content": []map[string]string{{"type": "input_text", "text": text}},
	})
	return []json.RawMessage{item}, nil
}

func sessionID(req request) string {
	if req.PromptCacheKey != "" {
		return req.PromptCacheKey
	}
	if v := stringField(req.ClientMetadata, "session_id", "sessionId", "sessionID", "thread_id", "threadId"); v != "" {
		return v
	}
	if v := stringField(req.Metadata, "session_id", "sessionId", "sessionID", "thread_id", "threadId"); v != "" {
		return v
	}
	return req.User
}

func stringField(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key].(string); ok {
			return v
		}
	}
	return ""
}

func requestTools(req request, items []json.RawMessage) []json.RawMessage {
	out := append([]json.RawMessage(nil), req.Tools...)
	for _, raw := range items {
		var item inputItem
		if json.Unmarshal(raw, &item) == nil && item.Type == "additional_tools" {
			out = append(out, item.Tools...)
		}
	}
	return out
}

func latestUserText(items []json.RawMessage) string {
	for i := len(items) - 1; i >= 0; i-- {
		var item inputItem
		if json.Unmarshal(items[i], &item) != nil || item.Role != "user" {
			continue
		}
		if text := contentText(item.Content); text != "" {
			return text
		}
	}
	return ""
}

func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if !isArray(raw) {
		var text string
		if json.Unmarshal(raw, &text) == nil {
			return text
		}
		return ""
	}
	var parts []contentPart
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	for _, part := range parts {
		switch part.Type {
		case "input_text", "output_text", "text":
			if part.Text != "" {
				return part.Text
			}
		}
	}
	return ""
}

func LatestUserText(body []byte) string {
	_, items, err := parseRequest(body)
	if err != nil {
		return ""
	}
	return latestUserText(items)
}

// Breakdown is the read-time itemization used by the request inspector.
type Breakdown struct {
	Tools    []ToolItem    `json:"tools"`
	System   []SystemItem  `json:"system"`
	Messages []MessageItem `json:"messages"`
	Flags    Flags         `json:"flags"`
}

type ToolItem struct {
	Name  string `json:"name"`
	Bytes int    `json:"bytes"`
}

type SystemItem struct {
	Bytes        int    `json:"bytes"`
	CacheControl string `json:"cacheControl"`
}

type MessageItem struct {
	Role       string   `json:"role"`
	Bytes      int      `json:"bytes"`
	BlockKinds []string `json:"blockKinds"`
}

type Flags struct {
	Thinking          bool `json:"thinking"`
	ContextManagement bool `json:"contextManagement"`
	OutputConfig      bool `json:"outputConfig"`
}

func BreakdownRequest(body []byte) (Breakdown, error) {
	req, items, err := parseRequest(body)
	if err != nil {
		return Breakdown{}, err
	}
	b := Breakdown{
		Tools:    []ToolItem{},
		System:   []SystemItem{},
		Messages: []MessageItem{},
		Flags: Flags{
			Thinking:          present(req.Reasoning),
			ContextManagement: present(req.ContextManagement),
			OutputConfig:      present(req.Text),
		},
	}
	for _, raw := range requestTools(req, items) {
		var t tool
		_ = json.Unmarshal(raw, &t)
		b.Tools = append(b.Tools, ToolItem{Name: t.Name, Bytes: len(raw)})
	}
	sort.SliceStable(b.Tools, func(i, j int) bool { return b.Tools[i].Bytes > b.Tools[j].Bytes })

	if present(req.Instructions) {
		b.System = append(b.System, SystemItem{Bytes: len(req.Instructions)})
	}
	for _, raw := range items {
		var item inputItem
		_ = json.Unmarshal(raw, &item)
		switch {
		case item.Type == "additional_tools":
			continue
		case item.Role == "system" || item.Role == "developer":
			b.System = append(b.System, SystemItem{Bytes: len(raw)})
		default:
			role := item.Role
			if role == "" {
				role = item.Type
			}
			b.Messages = append(b.Messages, MessageItem{
				Role:       role,
				Bytes:      len(raw),
				BlockKinds: blockKinds(item),
			})
		}
	}
	return b, nil
}

func blockKinds(item inputItem) []string {
	if isArray(item.Content) {
		var parts []contentPart
		if json.Unmarshal(item.Content, &parts) == nil {
			out := make([]string, 0, len(parts))
			for _, part := range parts {
				out = append(out, part.Type)
			}
			return out
		}
	}
	if len(item.Content) > 0 {
		return []string{"text"}
	}
	if item.Type != "" {
		return []string{item.Type}
	}
	return []string{}
}

// PrefixHashes makes the same diagnostic chain as the Anthropic adapter:
// tools, system/developer context, then each conversational input item.
func PrefixHashes(body []byte) []string {
	req, items, err := parseRequest(body)
	if err != nil {
		return nil
	}
	var tools, system []byte
	for _, raw := range requestTools(req, items) {
		tools = append(tools, raw...)
	}
	if present(req.Instructions) {
		system = append(system, req.Instructions...)
	}
	var messages [][]byte
	for _, raw := range items {
		var item inputItem
		_ = json.Unmarshal(raw, &item)
		switch {
		case item.Type == "additional_tools":
		case item.Role == "system" || item.Role == "developer":
			system = append(system, raw...)
		default:
			messages = append(messages, raw)
		}
	}

	out := make([]string, 0, 2+len(messages))
	var acc []byte
	step := func(segment []byte) {
		h := sha256.New()
		_, _ = h.Write(acc)
		_, _ = h.Write(segment)
		acc = h.Sum(nil)
		out = append(out, hex.EncodeToString(acc[:8]))
	}
	step(tools)
	step(system)
	for _, raw := range messages {
		step(raw)
	}
	return out
}

// Response is the subset of a completed Responses object needed to record
// facts and draw a session flow. The capture itself remains the full raw object.
type Response struct {
	ID                string            `json:"id"`
	Object            string            `json:"object"`
	Model             string            `json:"model"`
	Status            string            `json:"status"`
	Output            []OutputItem      `json:"output"`
	Usage             Usage             `json:"usage"`
	Error             *APIError         `json:"error"`
	IncompleteDetails *IncompleteDetail `json:"incomplete_details"`
}

type Usage struct {
	InputTokens        int64 `json:"input_tokens"`
	OutputTokens       int64 `json:"output_tokens"`
	InputTokensDetails struct {
		CachedTokens     int64 `json:"cached_tokens"`
		CacheWriteTokens int64 `json:"cache_write_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

type APIError struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type IncompleteDetail struct {
	Reason string `json:"reason"`
}

type OutputItem struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Role      string          `json:"role"`
	Content   []contentPart   `json:"content"`
	Summary   []contentPart   `json:"summary"`
}

type ResponseFacts struct {
	Model      string
	StopReason string
	Op         string
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
	Think      int64
	Text       int64
	Tool       int64
	ErrType    string
	ErrMsg     string
	Spawned    []string
}

func DecodeJSON(body []byte) (Response, ResponseFacts, error) {
	var resp Response
	if err := json.Unmarshal(body, &resp); err != nil {
		return Response{}, ResponseFacts{}, fmt.Errorf("response json: %w", err)
	}
	if resp.Object != "response" && resp.ID == "" && resp.Model == "" && len(resp.Output) == 0 && resp.Error == nil {
		return Response{}, ResponseFacts{}, errors.New("response json: not a Responses object")
	}
	return resp, factsOf(resp), nil
}

// DecodeSSE returns the final Responses object and its exact raw JSON. Unknown
// event types are ignored. If a stream ends early, the latest response envelope
// and any completed output items are retained.
func DecodeSSE(body []byte) (Response, ResponseFacts, []byte, error) {
	var latest Response
	var latestRaw []byte
	var items []OutputItem
	started := false

	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(line[len("data:"):])
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		var event struct {
			Type     string          `json:"type"`
			Response json.RawMessage `json:"response"`
			Item     json.RawMessage `json:"item"`
			Error    *APIError       `json:"error"`
		}
		if json.Unmarshal(data, &event) != nil {
			continue
		}
		if len(event.Response) > 0 {
			var resp Response
			if json.Unmarshal(event.Response, &resp) == nil {
				started = true
				latest = resp
				latestRaw = append(latestRaw[:0], event.Response...)
			}
		}
		if event.Type == "response.output_item.done" && len(event.Item) > 0 {
			var item OutputItem
			if json.Unmarshal(event.Item, &item) == nil {
				items = append(items, item)
			}
		}
		if event.Type == "error" && event.Error != nil {
			started = true
			latest.Error = event.Error
			latest.Status = "failed"
		}
	}
	if !started {
		return Response{}, ResponseFacts{}, nil, errors.New("no response event in stream")
	}
	if len(latest.Output) == 0 && len(items) > 0 {
		latest.Output = items
		latestRaw, _ = json.Marshal(latest)
	}
	if len(latestRaw) == 0 {
		latestRaw, _ = json.Marshal(latest)
	}
	return latest, factsOf(latest), latestRaw, nil
}

func factsOf(resp Response) ResponseFacts {
	read := resp.Usage.InputTokensDetails.CachedTokens
	write := resp.Usage.InputTokensDetails.CacheWriteTokens
	fresh := resp.Usage.InputTokens - read - write
	if fresh < 0 {
		fresh = 0
	}
	f := ResponseFacts{
		Model:      resp.Model,
		StopReason: stopReason(resp),
		Op:         opOf(resp.Output),
		Input:      fresh,
		Output:     resp.Usage.OutputTokens,
		CacheRead:  read,
		CacheWrite: write,
		Think:      min(resp.Usage.OutputTokensDetails.ReasoningTokens, resp.Usage.OutputTokens),
		Spawned:    AgentPrompts(resp.Output),
	}
	if resp.Error != nil {
		f.ErrType = firstNonempty(resp.Error.Type, resp.Error.Code, "upstream_error")
		f.ErrMsg = resp.Error.Message
	}
	visible := f.Output - f.Think
	var textBytes, toolBytes int64
	for _, call := range Calls(resp.Output) {
		toolBytes += int64(len(call.Name) + len(call.Arguments))
	}
	for _, item := range resp.Output {
		if item.Type == "message" {
			for _, part := range item.Content {
				textBytes += int64(len(part.Text))
			}
		}
	}
	switch total := textBytes + toolBytes; {
	case visible <= 0:
	case total == 0:
		f.Text = visible
	default:
		f.Tool = visible * toolBytes / total
		f.Text = visible - f.Tool
	}
	return f
}

func stopReason(resp Response) string {
	if resp.IncompleteDetails != nil && resp.IncompleteDetails.Reason != "" {
		if resp.IncompleteDetails.Reason == "max_output_tokens" {
			return "max_tokens"
		}
		return resp.IncompleteDetails.Reason
	}
	return resp.Status
}

func opOf(items []OutputItem) string {
	for _, call := range Calls(items) {
		op := "tool_use · " + call.Name
		if summary := argumentSummary(call.Arguments); summary != "" {
			op += " — " + truncate(summary, 24)
			if len([]rune(summary)) > 24 {
				op += "…"
			}
		}
		return op
	}
	return ""
}

type Call struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

func Calls(items []OutputItem) []Call {
	var out []Call
	for _, item := range items {
		name := item.Name
		switch item.Type {
		case "function_call", "custom_tool_call":
		case "local_shell_call":
			if name == "" {
				name = "shell"
			}
		case "computer_call":
			if name == "" {
				name = "computer"
			}
		case "mcp_call":
			if name == "" {
				name = "mcp"
			}
		case "web_search_call", "file_search_call", "code_interpreter_call":
			if name == "" {
				name = strings.TrimSuffix(item.Type, "_call")
			}
		default:
			continue
		}
		id := item.CallID
		if id == "" {
			id = item.ID
		}
		out = append(out, Call{ID: id, Name: name, Arguments: arguments(item.Arguments)})
	}
	return out
}

func ResponseCalls(body []byte) []Call {
	var resp Response
	if json.Unmarshal(body, &resp) != nil {
		return nil
	}
	return Calls(resp.Output)
}

func AgentPrompts(items []OutputItem) []string {
	return agentPrompts(Calls(items))
}

func agentPrompts(calls []Call) []string {
	var out []string
	for _, call := range calls {
		name := strings.ToLower(call.Name)
		if name != "task" && name != "agent" && name != "spawn_agent" && !strings.HasSuffix(name, ".spawn_agent") {
			continue
		}
		var args struct {
			Prompt  string `json:"prompt"`
			Message string `json:"message"`
			Task    string `json:"task"`
		}
		if json.Unmarshal(call.Arguments, &args) != nil {
			continue
		}
		if prompt := firstNonempty(args.Prompt, args.Message, args.Task); prompt != "" {
			out = append(out, prompt)
		}
	}
	return out
}

func DecodeError(status int, body []byte) (typ, message string) {
	var envelope struct {
		Error  *APIError       `json:"error"`
		Detail json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(body, &envelope) == nil {
		if envelope.Error != nil {
			return firstNonempty(envelope.Error.Type, envelope.Error.Code, fmt.Sprintf("http_%d", status)),
				firstNonempty(envelope.Error.Message, httpMessage(status))
		}
		if len(envelope.Detail) > 0 {
			var text string
			if json.Unmarshal(envelope.Detail, &text) == nil && text != "" {
				return fmt.Sprintf("http_%d", status), text
			}
		}
	}
	return fmt.Sprintf("http_%d", status), httpMessage(status)
}

type ResultItem struct {
	Name  string `json:"name"`
	Bytes int    `json:"bytes"`
	N     int    `json:"n"`
}

type ToolResultRef struct {
	ToolUseID string
	Bytes     int
}

func ToolResults(body []byte) []ToolResultRef {
	_, items, err := parseRequest(body)
	if err != nil {
		return nil
	}
	var out []ToolResultRef
	for i := len(items) - 1; i >= 0; i-- {
		var item inputItem
		if json.Unmarshal(items[i], &item) != nil || !isResultType(item.Type) {
			break
		}
		id := item.CallID
		if id == "" {
			id = item.ID
		}
		out = append(out, ToolResultRef{ToolUseID: id, Bytes: len(items[i])})
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func ResultsInContext(body []byte) []ResultItem {
	_, items, err := parseRequest(body)
	if err != nil {
		return nil
	}
	names := map[string]string{}
	by := map[string]*ResultItem{}
	for _, raw := range items {
		var item inputItem
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		switch {
		case isCallType(item.Type):
			id := item.CallID
			if id == "" {
				id = item.ID
			}
			if id != "" && item.Name != "" {
				names[id] = item.Name
			}
		case isResultType(item.Type):
			id := item.CallID
			if id == "" {
				id = item.ID
			}
			name := names[id]
			if name == "" {
				name = "(compacted away)"
			}
			it := by[name]
			if it == nil {
				it = &ResultItem{Name: name}
				by[name] = it
			}
			it.Bytes += len(raw)
			it.N++
		}
	}
	out := make([]ResultItem, 0, len(by))
	for _, item := range by {
		out = append(out, *item)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Bytes > out[j].Bytes })
	return out
}

func isCallType(typ string) bool {
	switch typ {
	case "function_call", "custom_tool_call", "local_shell_call", "computer_call", "mcp_call":
		return true
	default:
		return false
	}
}

func isResultType(typ string) bool {
	switch typ {
	case "function_call_output", "custom_tool_call_output", "local_shell_call_output", "computer_call_output", "mcp_call_output":
		return true
	default:
		return false
	}
}

func arguments(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if json.Valid([]byte(text)) {
			return json.RawMessage(text)
		}
		quoted, _ := json.Marshal(text)
		return quoted
	}
	return raw
}

func argumentSummary(raw json.RawMessage) string {
	raw = arguments(raw)
	var args struct {
		Command     string `json:"command"`
		Cmd         string `json:"cmd"`
		Path        string `json:"path"`
		Query       string `json:"query"`
		Prompt      string `json:"prompt"`
		Message     string `json:"message"`
		Description string `json:"description"`
	}
	if json.Unmarshal(raw, &args) != nil {
		return ""
	}
	for _, value := range []string{args.Command, args.Cmd, args.Path, args.Query, args.Prompt, args.Message, args.Description} {
		if value = strings.Join(strings.Fields(value), " "); value != "" {
			return value
		}
	}
	return ""
}

func httpMessage(status int) string {
	return fmt.Sprintf("upstream returned HTTP %d", status)
}

func present(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) && !bytes.Equal(trimmed, []byte("{}"))
}

func isArray(raw json.RawMessage) bool {
	for _, c := range raw {
		switch c {
		case ' ', '\t', '\r', '\n':
			continue
		default:
			return c == '['
		}
	}
	return false
}

func truncate(s string, maxLen int) string {
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen])
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
