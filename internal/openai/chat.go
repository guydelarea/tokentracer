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
)

// ChatRequestFacts is the normalized request shape for OpenAI-compatible Chat
// Completions endpoints. OpenCode uses this protocol for many non-OpenAI
// providers through the AI SDK's openai-compatible adapter.
type ChatRequestFacts struct {
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

type chatRequest struct {
	Model               string            `json:"model"`
	Stream              bool              `json:"stream"`
	MaxTokens           int               `json:"max_tokens"`
	MaxCompletionTokens int               `json:"max_completion_tokens"`
	Messages            []json.RawMessage `json:"messages"`
	Tools               []json.RawMessage `json:"tools"`
	User                string            `json:"user"`
	Metadata            map[string]any    `json:"metadata"`
	ClientMetadata      map[string]any    `json:"client_metadata"`
	Reasoning           json.RawMessage   `json:"reasoning"`
	ReasoningEffort     json.RawMessage   `json:"reasoning_effort"`
	ResponseFormat      json.RawMessage   `json:"response_format"`
}

type chatMessage struct {
	Role             string          `json:"role"`
	Content          json.RawMessage `json:"content"`
	ToolCalls        []chatToolCall  `json:"tool_calls"`
	ToolCallID       string          `json:"tool_call_id"`
	Name             string          `json:"name"`
	Reasoning        string          `json:"reasoning"`
	ReasoningContent string          `json:"reasoning_content"`
}

type chatTool struct {
	Type     string       `json:"type"`
	Name     string       `json:"name"`
	Function chatFunction `json:"function"`
}

type chatToolCall struct {
	Index    int          `json:"index"`
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func ParseChatRequest(body []byte) (ChatRequestFacts, error) {
	req, err := parseChatRequest(body)
	if err != nil {
		return ChatRequestFacts{}, err
	}
	f := ChatRequestFacts{
		Model:      req.Model,
		SessionID:  chatSessionID(req),
		Stream:     req.Stream,
		MaxTokens:  chatMaxTokens(req),
		ToolCount:  len(req.Tools),
		TotalBytes: len(body),
	}
	f.ParentSessionID = stringField(req.ClientMetadata, "parent_session_id", "parentSessionId", "parentSessionID")
	if f.ParentSessionID == "" {
		f.ParentSessionID = stringField(req.Metadata, "parent_session_id", "parentSessionId", "parentSessionID")
	}
	for _, raw := range req.Tools {
		f.ToolsBytes += len(raw)
	}
	for _, raw := range req.Messages {
		var message chatMessage
		if json.Unmarshal(raw, &message) != nil {
			f.MessagesBytes += len(raw)
			continue
		}
		switch message.Role {
		case "system", "developer":
			f.SystemBytes += len(raw)
		default:
			f.MessagesBytes += len(raw)
		}
		if message.Role == "user" {
			f.Turns++
		}
	}
	f.FirstText = latestChatUserText(req.Messages)
	f.Label = truncate(f.FirstText, labelMax)
	return f, nil
}

func parseChatRequest(body []byte) (chatRequest, error) {
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return chatRequest{}, fmt.Errorf("chat request json: %w", err)
	}
	if req.Messages == nil {
		return chatRequest{}, errors.New("chat request json: missing messages")
	}
	return req, nil
}

func chatSessionID(req chatRequest) string {
	if v := stringField(req.ClientMetadata, "session_id", "sessionId", "sessionID", "thread_id", "threadId"); v != "" {
		return v
	}
	if v := stringField(req.Metadata, "session_id", "sessionId", "sessionID", "thread_id", "threadId"); v != "" {
		return v
	}
	return req.User
}

func chatMaxTokens(req chatRequest) int {
	if req.MaxCompletionTokens != 0 {
		return req.MaxCompletionTokens
	}
	return req.MaxTokens
}

func latestChatUserText(messages []json.RawMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		var message chatMessage
		if json.Unmarshal(messages[i], &message) != nil || message.Role != "user" {
			continue
		}
		if text := contentText(message.Content); text != "" {
			return text
		}
	}
	return ""
}

func LatestChatUserText(body []byte) string {
	req, err := parseChatRequest(body)
	if err != nil {
		return ""
	}
	return latestChatUserText(req.Messages)
}

func BreakdownChatRequest(body []byte) (Breakdown, error) {
	req, err := parseChatRequest(body)
	if err != nil {
		return Breakdown{}, err
	}
	b := Breakdown{
		Tools:    []ToolItem{},
		System:   []SystemItem{},
		Messages: []MessageItem{},
		Flags: Flags{
			Thinking:     present(req.Reasoning) || present(req.ReasoningEffort),
			OutputConfig: present(req.ResponseFormat),
		},
	}
	for _, raw := range req.Tools {
		var tool chatTool
		_ = json.Unmarshal(raw, &tool)
		name := tool.Function.Name
		if name == "" {
			name = tool.Name
		}
		b.Tools = append(b.Tools, ToolItem{Name: name, Bytes: len(raw)})
	}
	sort.SliceStable(b.Tools, func(i, j int) bool { return b.Tools[i].Bytes > b.Tools[j].Bytes })
	for _, raw := range req.Messages {
		var message chatMessage
		_ = json.Unmarshal(raw, &message)
		if message.Role == "system" || message.Role == "developer" {
			b.System = append(b.System, SystemItem{Bytes: len(raw)})
			continue
		}
		role := message.Role
		if role == "" {
			role = "message"
		}
		b.Messages = append(b.Messages, MessageItem{
			Role:       role,
			Bytes:      len(raw),
			BlockKinds: chatBlockKinds(message),
		})
	}
	return b, nil
}

func chatBlockKinds(message chatMessage) []string {
	if len(message.ToolCalls) > 0 {
		return []string{"function_call"}
	}
	if message.Role == "tool" || message.Role == "function" {
		return []string{"function_result"}
	}
	if message.Reasoning != "" || message.ReasoningContent != "" {
		return []string{"reasoning"}
	}
	if isArray(message.Content) {
		var parts []contentPart
		if json.Unmarshal(message.Content, &parts) == nil {
			out := make([]string, 0, len(parts))
			for _, part := range parts {
				out = append(out, part.Type)
			}
			return out
		}
	}
	if len(message.Content) > 0 {
		return []string{"text"}
	}
	return []string{}
}

// ChatPrefixHashes builds the same diagnostic chain as the Responses adapter:
// tool schemas, system/developer context, then each conversational message.
func ChatPrefixHashes(body []byte) []string {
	req, err := parseChatRequest(body)
	if err != nil {
		return nil
	}
	var tools, system []byte
	var messages [][]byte
	for _, raw := range req.Tools {
		tools = append(tools, raw...)
	}
	for _, raw := range req.Messages {
		var message chatMessage
		_ = json.Unmarshal(raw, &message)
		if message.Role == "system" || message.Role == "developer" {
			system = append(system, raw...)
		} else {
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

// ChatResponse is the assembled representation of a Chat Completions response.
// For streams, each choice's delta is merged into its final message before it is
// stored as the capture.
type ChatResponse struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Model   string       `json:"model"`
	Choices []ChatChoice `json:"choices"`
	Usage   ChatUsage    `json:"usage"`
	Error   *APIError    `json:"error"`
}

type ChatChoice struct {
	Index        int         `json:"index"`
	FinishReason string      `json:"finish_reason"`
	Message      chatMessage `json:"message"`
	Delta        chatMessage `json:"delta,omitempty"`
}

type ChatUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	PromptDetails    struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

// ChatResponseFacts is the response projection used by the recorder.
type ChatResponseFacts struct {
	Model      string
	StopReason string
	Op         string
	Input      int64
	Output     int64
	CacheRead  int64
	Think      int64
	Text       int64
	Tool       int64
	ErrType    string
	ErrMsg     string
	Spawned    []string
}

func DecodeChatJSON(body []byte) (ChatResponse, ChatResponseFacts, error) {
	var response ChatResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return ChatResponse{}, ChatResponseFacts{}, fmt.Errorf("chat response json: %w", err)
	}
	if response.ID == "" && response.Model == "" && len(response.Choices) == 0 && response.Error == nil {
		return ChatResponse{}, ChatResponseFacts{}, errors.New("chat response json: not a Chat Completions object")
	}
	return response, chatFactsOf(response), nil
}

func DecodeChatSSE(body []byte) (ChatResponse, ChatResponseFacts, []byte, error) {
	var response ChatResponse
	choices := map[int]*ChatChoice{}
	started := false

	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(line[len("data:"):])
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		var event ChatResponse
		if json.Unmarshal(data, &event) != nil {
			continue
		}
		if event.ID == "" && event.Model == "" && len(event.Choices) == 0 && event.Error == nil && event.Usage.PromptTokens == 0 && event.Usage.CompletionTokens == 0 {
			continue
		}
		started = true
		if event.ID != "" {
			response.ID = event.ID
		}
		if event.Object != "" {
			response.Object = event.Object
		}
		if event.Model != "" {
			response.Model = event.Model
		}
		if event.Error != nil {
			response.Error = event.Error
		}
		if event.Usage.PromptTokens != 0 || event.Usage.CompletionTokens != 0 || event.Usage.PromptDetails.CachedTokens != 0 {
			response.Usage = event.Usage
		}
		for _, incoming := range event.Choices {
			choice := choices[incoming.Index]
			if choice == nil {
				response.Choices = append(response.Choices, ChatChoice{Index: incoming.Index})
				choice = &response.Choices[len(response.Choices)-1]
				choices[incoming.Index] = choice
			}
			mergeChatChoice(choice, incoming)
		}
	}
	if err := scanner.Err(); err != nil {
		return ChatResponse{}, ChatResponseFacts{}, nil, fmt.Errorf("chat response stream: %w", err)
	}
	if !started {
		return ChatResponse{}, ChatResponseFacts{}, nil, errors.New("no Chat Completions event in stream")
	}
	response.Object = "chat.completion"
	raw, err := json.Marshal(response)
	if err != nil {
		return ChatResponse{}, ChatResponseFacts{}, nil, fmt.Errorf("marshal Chat Completions response: %w", err)
	}
	return response, chatFactsOf(response), raw, nil
}

func mergeChatChoice(dst *ChatChoice, incoming ChatChoice) {
	if incoming.FinishReason != "" {
		dst.FinishReason = incoming.FinishReason
	}
	message := incoming.Delta
	if !chatMessageHasData(message) {
		message = incoming.Message
	}
	if message.Role != "" {
		dst.Message.Role = message.Role
	}
	dst.Message.Content = appendChatText(dst.Message.Content, message.Content)
	if message.Reasoning != "" {
		dst.Message.Reasoning += message.Reasoning
	}
	if message.ReasoningContent != "" {
		dst.Message.ReasoningContent += message.ReasoningContent
	}
	for i, call := range message.ToolCalls {
		index := call.Index
		if index == 0 && i > 0 {
			index = i
		}
		for len(dst.Message.ToolCalls) <= index {
			dst.Message.ToolCalls = append(dst.Message.ToolCalls, chatToolCall{})
		}
		merged := &dst.Message.ToolCalls[index]
		if call.ID != "" {
			merged.ID = call.ID
		}
		if call.Type != "" {
			merged.Type = call.Type
		}
		if call.Function.Name != "" {
			merged.Function.Name = call.Function.Name
		}
		merged.Function.Arguments = appendChatText(merged.Function.Arguments, call.Function.Arguments)
	}
}

func chatMessageHasData(message chatMessage) bool {
	return message.Role != "" || len(message.Content) > 0 || len(message.ToolCalls) > 0 || message.Reasoning != "" || message.ReasoningContent != ""
}

func appendChatText(old, next json.RawMessage) json.RawMessage {
	if len(next) == 0 || bytes.Equal(bytes.TrimSpace(next), []byte("null")) {
		return old
	}
	if len(old) == 0 || bytes.Equal(bytes.TrimSpace(old), []byte("null")) {
		return append(json.RawMessage(nil), next...)
	}
	var left, right string
	if json.Unmarshal(old, &left) == nil && json.Unmarshal(next, &right) == nil {
		joined, _ := json.Marshal(left + right)
		return joined
	}
	return append(json.RawMessage(nil), next...)
}

func chatFactsOf(response ChatResponse) ChatResponseFacts {
	read := response.Usage.PromptDetails.CachedTokens
	fresh := response.Usage.PromptTokens - read
	if fresh < 0 {
		fresh = 0
	}
	f := ChatResponseFacts{
		Model:      response.Model,
		StopReason: chatStopReason(response),
		Input:      fresh,
		Output:     response.Usage.CompletionTokens,
		CacheRead:  read,
		Think:      min(response.Usage.CompletionDetails.ReasoningTokens, response.Usage.CompletionTokens),
		Spawned:    chatAgentPrompts(response),
	}
	if response.Error != nil {
		f.ErrType = firstNonempty(response.Error.Type, response.Error.Code, "upstream_error")
		f.ErrMsg = response.Error.Message
	}
	calls := chatCalls(response)
	if len(calls) > 0 {
		f.Op = chatOp(calls[0])
	}
	visible := f.Output - f.Think
	var textBytes, toolBytes int64
	for _, choice := range response.Choices {
		textBytes += int64(len(choice.Message.Content))
	}
	for _, call := range calls {
		toolBytes += int64(len(call.Name) + len(call.Arguments))
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

func chatStopReason(response ChatResponse) string {
	for _, choice := range response.Choices {
		if choice.FinishReason == "length" {
			return "max_tokens"
		}
		if choice.FinishReason != "" {
			return choice.FinishReason
		}
	}
	if response.Error != nil {
		return "failed"
	}
	return ""
}

func chatOp(call Call) string {
	op := "tool_use · " + call.Name
	if summary := argumentSummary(call.Arguments); summary != "" {
		op += " — " + truncate(summary, 24)
		if len([]rune(summary)) > 24 {
			op += "…"
		}
	}
	return op
}

func chatCalls(response ChatResponse) []Call {
	var out []Call
	for _, choice := range response.Choices {
		for _, call := range choice.Message.ToolCalls {
			if call.Function.Name == "" {
				continue
			}
			out = append(out, Call{ID: call.ID, Name: call.Function.Name, Arguments: arguments(call.Function.Arguments)})
		}
	}
	return out
}

func ChatResponseCalls(body []byte) []Call {
	var response ChatResponse
	if json.Unmarshal(body, &response) != nil {
		return nil
	}
	return chatCalls(response)
}

func chatAgentPrompts(response ChatResponse) []string {
	return agentPrompts(chatCalls(response))
}

func ChatToolResults(body []byte) []ToolResultRef {
	req, err := parseChatRequest(body)
	if err != nil {
		return nil
	}
	var out []ToolResultRef
	for i := len(req.Messages) - 1; i >= 0; i-- {
		var message chatMessage
		if json.Unmarshal(req.Messages[i], &message) != nil || (message.Role != "tool" && message.Role != "function") {
			break
		}
		id := message.ToolCallID
		if id == "" {
			id = message.Name
		}
		out = append(out, ToolResultRef{ToolUseID: id, Bytes: len(req.Messages[i])})
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func ChatResultsInContext(body []byte) []ResultItem {
	req, err := parseChatRequest(body)
	if err != nil {
		return nil
	}
	names := map[string]string{}
	by := map[string]*ResultItem{}
	for _, raw := range req.Messages {
		var message chatMessage
		if json.Unmarshal(raw, &message) != nil {
			continue
		}
		if message.Role == "assistant" {
			for _, call := range message.ToolCalls {
				if call.ID != "" && call.Function.Name != "" {
					names[call.ID] = call.Function.Name
				}
			}
			continue
		}
		if message.Role != "tool" && message.Role != "function" {
			continue
		}
		id := message.ToolCallID
		name := names[id]
		if name == "" {
			name = message.Name
		}
		if name == "" {
			name = "(compacted away)"
		}
		item := by[name]
		if item == nil {
			item = &ResultItem{Name: name}
			by[name] = item
		}
		item.Bytes += len(raw)
		item.N++
	}
	out := make([]ResultItem, 0, len(by))
	for _, item := range by {
		out = append(out, *item)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Bytes > out[j].Bytes })
	return out
}
