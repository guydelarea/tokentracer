// Package wire is the seam between TokenTracer's Recorder/dashboard and an
// upstream wire dialect. Callers ask for normalized observations and request
// inspections; the Anthropic and OpenAI adapters keep their wire knowledge
// local to their own packages.
package wire

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/guydelarea/tokentracer/internal/anthropic"
	"github.com/guydelarea/tokentracer/internal/openai"
)

type Kind string

const (
	Unknown    Kind = ""
	Anthropic  Kind = "anthropic"
	OpenAI     Kind = "openai"
	OpenAIChat Kind = "openai_chat"
)

type Observation struct {
	Kind Kind

	Request    RequestFacts
	RequestErr error

	Response        ResponseFacts
	ResponseErr     error
	ResponseCapture []byte

	// Problem is an upstream failure, either an HTTP error or an error carried
	// inside a nominally successful stream.
	Problem *Problem
}

type Problem struct {
	Type    string
	Message string
}

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
	Prefix          []string
}

type ResponseFacts struct {
	Model      string
	StopReason string
	Op         string
	Input      int64
	Output     int64
	CacheRead  int64
	CacheW5m   int64
	CacheW1h   int64
	Think      int64
	Text       int64
	Tool       int64
	Spawned    []string
}

// Recordable reports whether this request is a billed model exchange that the
// Recorder understands. Auxiliary endpoints still pass through the proxy.
func Recordable(method, path string) bool {
	if method != http.MethodPost {
		return false
	}
	return KindForPath(path) != Unknown
}

func KindForPath(path string) Kind {
	if path == "/v1/messages" {
		return Anthropic
	}
	if model := anthropic.VertexModel(path); model != "" && model != "count-tokens" {
		return Anthropic
	}
	// OpenCode uses this protocol for the broad set of providers backed by the
	// AI SDK's openai-compatible adapter. Match the suffix so Azure deployment
	// paths and provider-specific prefixes work too.
	if strings.HasSuffix(strings.TrimSuffix(path, "/"), "/chat/completions") {
		return OpenAIChat
	}
	// A caller's configured base URL decides whether this arrives as
	// /responses, /v1/responses, or /backend-api/codex/responses.
	if strings.HasSuffix(strings.TrimSuffix(path, "/"), "/responses") {
		return OpenAI
	}
	return Unknown
}

// ObserveRequest parses only the request half of an exchange. Keeping this
// operation separate from ObserveResponse is deliberate: the Recorder must
// retain valid usage even when request parsing fails or panics.
func ObserveRequest(path string, body []byte) Observation {
	kind := KindForPath(path)
	obs := Observation{Kind: kind}
	switch kind {
	case Anthropic:
		facts, err := anthropic.ParseRequest(body)
		if err != nil {
			obs.RequestErr = err
			break
		}
		obs.Request = RequestFacts{
			Model: facts.Model, SessionID: facts.SessionID, ParentSessionID: facts.ParentSessionID,
			Stream: facts.Stream, Turns: facts.Turns, ToolCount: facts.ToolCount, MaxTokens: facts.MaxTokens,
			TotalBytes: facts.TotalBytes, ToolsBytes: facts.ToolsBytes, SystemBytes: facts.SystemBytes,
			MessagesBytes: facts.MessagesBytes, Label: facts.Label, FirstText: facts.FirstText,
			Prefix: anthropic.PrefixHashes(body),
		}
		if obs.Request.Model == "" {
			obs.Request.Model = anthropic.VertexModel(path)
		}
	case OpenAI:
		facts, err := openai.ParseRequest(body)
		if err != nil {
			obs.RequestErr = err
			break
		}
		obs.Request = RequestFacts{
			Model: facts.Model, SessionID: facts.SessionID, ParentSessionID: facts.ParentSessionID,
			Stream: facts.Stream, Turns: facts.Turns, ToolCount: facts.ToolCount, MaxTokens: facts.MaxTokens,
			TotalBytes: facts.TotalBytes, ToolsBytes: facts.ToolsBytes, SystemBytes: facts.SystemBytes,
			MessagesBytes: facts.MessagesBytes, Label: facts.Label, FirstText: facts.FirstText,
			Prefix: openai.PrefixHashes(body),
		}
	case OpenAIChat:
		facts, err := openai.ParseChatRequest(body)
		if err != nil {
			obs.RequestErr = err
			break
		}
		obs.Request = RequestFacts{
			Model: facts.Model, SessionID: facts.SessionID, ParentSessionID: facts.ParentSessionID,
			Stream: facts.Stream, Turns: facts.Turns, ToolCount: facts.ToolCount, MaxTokens: facts.MaxTokens,
			TotalBytes: facts.TotalBytes, ToolsBytes: facts.ToolsBytes, SystemBytes: facts.SystemBytes,
			MessagesBytes: facts.MessagesBytes, Label: facts.Label, FirstText: facts.FirstText,
			Prefix: openai.ChatPrefixHashes(body),
		}
	default:
		obs.RequestErr = errors.New("unknown vendor path")
	}
	return obs
}

// ObserveResponse parses only the response half and returns the compact capture
// representation that should be stored. For streams that is the assembled final
// response, not the event transcript.
func ObserveResponse(path string, status int, streamed bool, body []byte) Observation {
	kind := KindForPath(path)
	obs := Observation{Kind: kind, ResponseCapture: body}
	switch kind {
	case Anthropic:
		if status >= 400 {
			typ, message := anthropic.DecodeError(status, body)
			obs.Problem = &Problem{Type: typ, Message: message}
			break
		}
		var resp anthropic.Response
		var err error
		if streamed {
			resp, err = anthropic.DecodeSSE(body)
		} else {
			resp, err = anthropic.DecodeJSON(body)
		}
		if err != nil {
			obs.ResponseErr = err
			break
		}
		obs.Response = ResponseFacts{
			Model: resp.Model, StopReason: resp.StopReason, Op: resp.Op(),
			Input: resp.Usage.In, Output: resp.Usage.Out, CacheRead: resp.Usage.CacheRead,
			CacheW5m: resp.Usage.W5m, CacheW1h: resp.Usage.W1h,
			Spawned: anthropic.AgentPrompts(resp.Content),
		}
		obs.Response.Think, obs.Response.Text, obs.Response.Tool = anthropic.SplitOutput(resp.Usage.Out, resp.Content)
		if resp.ErrType != "" {
			obs.Problem = &Problem{Type: resp.ErrType, Message: resp.ErrMsg}
		}
		if assembled, marshalErr := json.Marshal(resp); marshalErr == nil {
			obs.ResponseCapture = assembled
		}
	case OpenAI:
		if status >= 400 {
			typ, message := openai.DecodeError(status, body)
			obs.Problem = &Problem{Type: typ, Message: message}
			break
		}
		var responseFacts openai.ResponseFacts
		var err error
		if streamed {
			_, responseFacts, obs.ResponseCapture, err = openai.DecodeSSE(body)
		} else {
			_, responseFacts, err = openai.DecodeJSON(body)
		}
		if err != nil {
			obs.ResponseErr = err
			break
		}
		obs.Response = ResponseFacts{
			Model: responseFacts.Model, StopReason: responseFacts.StopReason, Op: responseFacts.Op,
			Input: responseFacts.Input, Output: responseFacts.Output, CacheRead: responseFacts.CacheRead,
			CacheW5m: responseFacts.CacheWrite, Think: responseFacts.Think, Text: responseFacts.Text,
			Tool: responseFacts.Tool, Spawned: responseFacts.Spawned,
		}
		if responseFacts.ErrType != "" {
			obs.Problem = &Problem{Type: responseFacts.ErrType, Message: responseFacts.ErrMsg}
		}
	case OpenAIChat:
		if status >= 400 {
			typ, message := openai.DecodeError(status, body)
			obs.Problem = &Problem{Type: typ, Message: message}
			break
		}
		var responseFacts openai.ChatResponseFacts
		var err error
		if streamed {
			_, responseFacts, obs.ResponseCapture, err = openai.DecodeChatSSE(body)
		} else {
			_, responseFacts, err = openai.DecodeChatJSON(body)
		}
		if err != nil {
			obs.ResponseErr = err
			break
		}
		obs.Response = ResponseFacts{
			Model: responseFacts.Model, StopReason: responseFacts.StopReason, Op: responseFacts.Op,
			Input: responseFacts.Input, Output: responseFacts.Output, CacheRead: responseFacts.CacheRead,
			Think: responseFacts.Think, Text: responseFacts.Text, Tool: responseFacts.Tool,
			Spawned: responseFacts.Spawned,
		}
		if responseFacts.ErrType != "" {
			obs.Problem = &Problem{Type: responseFacts.ErrType, Message: responseFacts.ErrMsg}
		}
	default:
		obs.ResponseErr = errors.New("unknown vendor path")
	}
	return obs
}

type RequestInspection struct {
	Kind           Kind
	Facts          RequestFacts
	Breakdown      Breakdown
	LatestUserText string
	Results        []ResultItem
	ToolResults    []ToolResultRef
}

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

type ResultItem struct {
	Name  string `json:"name"`
	Bytes int    `json:"bytes"`
	N     int    `json:"n"`
}

type ToolResultRef struct {
	ToolUseID string
	Bytes     int
}

// InspectRequest performs every on-demand interpretation the dashboard needs
// in one parse-oriented module call.
func InspectRequest(body []byte) (RequestInspection, error) {
	return inspectRequest(kindForRequest(body), body)
}

// InspectRequestAt uses the endpoint that carried a capture when it is known.
// Chat Completions and Anthropic Messages both carry a top-level messages field,
// so the path is the authoritative dialect discriminator.
func InspectRequestAt(path string, body []byte) (RequestInspection, error) {
	kind := KindForPath(path)
	if kind == Unknown {
		kind = kindForRequest(body)
	}
	return inspectRequest(kind, body)
}

func inspectRequest(kind Kind, body []byte) (RequestInspection, error) {
	switch kind {
	case Anthropic:
		facts, err := anthropic.ParseRequest(body)
		if err != nil {
			return RequestInspection{}, err
		}
		bd, err := anthropic.BreakdownRequest(body)
		if err != nil {
			return RequestInspection{}, err
		}
		view := RequestInspection{
			Kind: kind,
			Facts: RequestFacts{
				Model: facts.Model, SessionID: facts.SessionID, ParentSessionID: facts.ParentSessionID,
				Stream: facts.Stream, Turns: facts.Turns, ToolCount: facts.ToolCount, MaxTokens: facts.MaxTokens,
				TotalBytes: facts.TotalBytes, ToolsBytes: facts.ToolsBytes, SystemBytes: facts.SystemBytes,
				MessagesBytes: facts.MessagesBytes, Label: facts.Label, FirstText: facts.FirstText,
				Prefix: anthropic.PrefixHashes(body),
			},
			Breakdown: Breakdown{
				Tools: convertAnthropicTools(bd.Tools), System: convertAnthropicSystem(bd.System),
				Messages: convertAnthropicMessages(bd.Messages),
				Flags: Flags{
					Thinking: bd.Flags.Thinking, ContextManagement: bd.Flags.ContextManagement,
					OutputConfig: bd.Flags.OutputConfig,
				},
			},
			LatestUserText: anthropic.LatestUserText(body),
		}
		for _, item := range anthropic.ResultsInContext(body) {
			view.Results = append(view.Results, ResultItem{Name: item.Name, Bytes: item.Bytes, N: item.N})
		}
		for _, item := range anthropic.ToolResults(body) {
			view.ToolResults = append(view.ToolResults, ToolResultRef{ToolUseID: item.ToolUseID, Bytes: item.Bytes})
		}
		return view, nil

	case OpenAI:
		facts, err := openai.ParseRequest(body)
		if err != nil {
			return RequestInspection{}, err
		}
		bd, err := openai.BreakdownRequest(body)
		if err != nil {
			return RequestInspection{}, err
		}
		view := RequestInspection{
			Kind: kind,
			Facts: RequestFacts{
				Model: facts.Model, SessionID: facts.SessionID, ParentSessionID: facts.ParentSessionID,
				Stream: facts.Stream, Turns: facts.Turns, ToolCount: facts.ToolCount, MaxTokens: facts.MaxTokens,
				TotalBytes: facts.TotalBytes, ToolsBytes: facts.ToolsBytes, SystemBytes: facts.SystemBytes,
				MessagesBytes: facts.MessagesBytes, Label: facts.Label, FirstText: facts.FirstText,
				Prefix: openai.PrefixHashes(body),
			},
			Breakdown: Breakdown{
				Tools: convertOpenAITools(bd.Tools), System: convertOpenAISystem(bd.System),
				Messages: convertOpenAIMessages(bd.Messages),
				Flags: Flags{
					Thinking: bd.Flags.Thinking, ContextManagement: bd.Flags.ContextManagement,
					OutputConfig: bd.Flags.OutputConfig,
				},
			},
			LatestUserText: openai.LatestUserText(body),
		}
		for _, item := range openai.ResultsInContext(body) {
			view.Results = append(view.Results, ResultItem{Name: item.Name, Bytes: item.Bytes, N: item.N})
		}
		for _, item := range openai.ToolResults(body) {
			view.ToolResults = append(view.ToolResults, ToolResultRef{ToolUseID: item.ToolUseID, Bytes: item.Bytes})
		}
		return view, nil

	case OpenAIChat:
		facts, err := openai.ParseChatRequest(body)
		if err != nil {
			return RequestInspection{}, err
		}
		bd, err := openai.BreakdownChatRequest(body)
		if err != nil {
			return RequestInspection{}, err
		}
		view := RequestInspection{
			Kind: kind,
			Facts: RequestFacts{
				Model: facts.Model, SessionID: facts.SessionID, ParentSessionID: facts.ParentSessionID,
				Stream: facts.Stream, Turns: facts.Turns, ToolCount: facts.ToolCount, MaxTokens: facts.MaxTokens,
				TotalBytes: facts.TotalBytes, ToolsBytes: facts.ToolsBytes, SystemBytes: facts.SystemBytes,
				MessagesBytes: facts.MessagesBytes, Label: facts.Label, FirstText: facts.FirstText,
				Prefix: openai.ChatPrefixHashes(body),
			},
			Breakdown: Breakdown{
				Tools: convertOpenAITools(bd.Tools), System: convertOpenAISystem(bd.System),
				Messages: convertOpenAIMessages(bd.Messages),
				Flags: Flags{
					Thinking: bd.Flags.Thinking, ContextManagement: bd.Flags.ContextManagement,
					OutputConfig: bd.Flags.OutputConfig,
				},
			},
			LatestUserText: openai.LatestChatUserText(body),
		}
		for _, item := range openai.ChatResultsInContext(body) {
			view.Results = append(view.Results, ResultItem{Name: item.Name, Bytes: item.Bytes, N: item.N})
		}
		for _, item := range openai.ChatToolResults(body) {
			view.ToolResults = append(view.ToolResults, ToolResultRef{ToolUseID: item.ToolUseID, Bytes: item.Bytes})
		}
		return view, nil
	default:
		return RequestInspection{}, errors.New("unknown request dialect")
	}
}

func kindForRequest(body []byte) Kind {
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &top) != nil {
		return Unknown
	}
	if _, ok := top["input"]; ok {
		return OpenAI
	}
	if _, ok := top["messages"]; ok {
		if isChatRequest(top) {
			return OpenAIChat
		}
		return Anthropic
	}
	return Unknown
}

func isChatRequest(top map[string]json.RawMessage) bool {
	for _, key := range []string{"max_completion_tokens", "stream_options", "response_format", "functions", "function_call"} {
		if _, ok := top[key]; ok {
			return true
		}
	}
	var tools []json.RawMessage
	if raw, ok := top["tools"]; ok && json.Unmarshal(raw, &tools) == nil {
		for _, raw := range tools {
			var tool struct {
				Function json.RawMessage `json:"function"`
			}
			if json.Unmarshal(raw, &tool) == nil && len(tool.Function) > 0 {
				return true
			}
		}
	}
	var messages []json.RawMessage
	if raw, ok := top["messages"]; !ok || json.Unmarshal(raw, &messages) != nil {
		return false
	}
	for _, raw := range messages {
		var message struct {
			Role string `json:"role"`
		}
		if json.Unmarshal(raw, &message) == nil && (message.Role == "developer" || message.Role == "tool") {
			return true
		}
	}
	return false
}

type Call struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// ResponseCalls reads tool calls from an assembled response capture.
func ResponseCalls(body []byte) []Call {
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &top) != nil {
		return nil
	}
	if _, ok := top["output"]; ok {
		var out []Call
		for _, call := range openai.ResponseCalls(body) {
			out = append(out, Call{ID: call.ID, Name: call.Name, Arguments: call.Arguments})
		}
		return out
	}
	if _, ok := top["choices"]; ok {
		var out []Call
		for _, call := range openai.ChatResponseCalls(body) {
			out = append(out, Call{ID: call.ID, Name: call.Name, Arguments: call.Arguments})
		}
		return out
	}
	var resp anthropic.Response
	if json.Unmarshal(body, &resp) != nil {
		return nil
	}
	var out []Call
	for _, block := range resp.Content {
		if block.Type == "tool_use" && block.Name != "" {
			out = append(out, Call{ID: block.ID, Name: block.Name, Arguments: block.Input})
		}
	}
	return out
}

func FirstDiff(a, b []string) int {
	n := min(len(a), len(b))
	for i := range n {
		if a[i] != b[i] {
			return i
		}
	}
	return -1
}

func convertAnthropicTools(in []anthropic.ToolItem) []ToolItem {
	out := make([]ToolItem, 0, len(in))
	for _, item := range in {
		out = append(out, ToolItem{Name: item.Name, Bytes: item.Bytes})
	}
	return out
}

func convertAnthropicSystem(in []anthropic.SystemItem) []SystemItem {
	out := make([]SystemItem, 0, len(in))
	for _, item := range in {
		out = append(out, SystemItem{Bytes: item.Bytes, CacheControl: item.CacheControl})
	}
	return out
}

func convertAnthropicMessages(in []anthropic.MessageItem) []MessageItem {
	out := make([]MessageItem, 0, len(in))
	for _, item := range in {
		out = append(out, MessageItem{Role: item.Role, Bytes: item.Bytes, BlockKinds: item.BlockKinds})
	}
	return out
}

func convertOpenAITools(in []openai.ToolItem) []ToolItem {
	out := make([]ToolItem, 0, len(in))
	for _, item := range in {
		out = append(out, ToolItem{Name: item.Name, Bytes: item.Bytes})
	}
	return out
}

func convertOpenAISystem(in []openai.SystemItem) []SystemItem {
	out := make([]SystemItem, 0, len(in))
	for _, item := range in {
		out = append(out, SystemItem{Bytes: item.Bytes, CacheControl: item.CacheControl})
	}
	return out
}

func convertOpenAIMessages(in []openai.MessageItem) []MessageItem {
	out := make([]MessageItem, 0, len(in))
	for _, item := range in {
		out = append(out, MessageItem{Role: item.Role, Bytes: item.Bytes, BlockKinds: item.BlockKinds})
	}
	return out
}
