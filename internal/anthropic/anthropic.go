// Package anthropic is the vendor module for the Anthropic Messages API: pure
// functions, no I/O. It reads what it knows and never re-serializes the request
// — the capture keeps the verbatim bytes, so unknown fields round-trip unharmed.
//
// Forward-compatibility is a rule here, not a nicety: unknown request fields,
// unknown SSE events, and unknown block types are skipped, never fatal. A body
// we half-understand still yields the facts we did understand.
package anthropic

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// Request side
// ---------------------------------------------------------------------------

// RequestFacts is everything the fact row needs from a request body.
//
// The byte columns are measured on the *verbatim* bytes of each section as they
// arrived — never on a re-marshaled copy. That keeps them honest (they are what
// went over the wire) and makes the breakdown's per-item sizes sum exactly to
// them, because both read the same raw slices.
type RequestFacts struct {
	Model     string // "" when the body named none — Vertex puts it in the URL instead
	SessionID string // "" when the client sent no recognizable session id

	// ParentSessionID is the session that spawned this one, as the client itself
	// reported it. "" when the client sent none, which is what a top-level session
	// looks like — and also what an older client looks like.
	ParentSessionID string

	Stream    bool
	Turns     int
	ToolCount int
	MaxTokens int // the request's own output cap; 0 when it named none

	TotalBytes    int // the whole body
	ToolsBytes    int // Σ per-tool raw bytes
	SystemBytes   int // Σ per-system-block raw bytes (or the raw string)
	MessagesBytes int // Σ per-message raw bytes

	Label string // first user-role text, ≤64 chars

	// FirstText is the same first user text, whole. Never stored: it exists so
	// the recorder can match a new session's opening message against the Task
	// prompts it has seen leave in tool_use blocks — which is the only thread on
	// the wire tying a subagent's session to the session that spawned it.
	FirstText string
}

// request is the shape we read. Sections stay raw: we measure and itemize them,
// we never rebuild them.
type request struct {
	Model     string            `json:"model"`
	Stream    bool              `json:"stream"`
	MaxTokens int               `json:"max_tokens"`
	System    json.RawMessage   `json:"system"`
	Tools     []json.RawMessage `json:"tools"`
	Messages  []json.RawMessage `json:"messages"`
	Metadata  struct {
		UserID string `json:"user_id"`
	} `json:"metadata"`

	Thinking          json.RawMessage `json:"thinking"`
	ContextManagement json.RawMessage `json:"context_management"`
	OutputConfig      json.RawMessage `json:"output_config"`
}

// message content is a raw string OR a block array — the real data does both,
// in the same conversation.
type message struct {
	Role    string          `json:"role"` // user, assistant, and "system" mid-conversation
	Content json.RawMessage `json:"content"`
}

type block struct {
	Type         string          `json:"type"`
	Text         string          `json:"text"`
	CacheControl json.RawMessage `json:"cache_control"`
}

type tool struct {
	Name string `json:"name"`
}

// Claude Code packs a JSON document into the metadata.user_id *string*.
//
// It carries the parent session too, so a subagent's link to the session that
// spawned it is a fact on the wire — not something to infer. sessionRe cannot
// match parent_session_id by accident: it anchors on the opening quote, and the
// character before "session_id" inside "parent_session_id" is an underscore.
var (
	sessionRe       = regexp.MustCompile(`"session_id"\s*:\s*"([^"]+)"`)
	parentSessionRe = regexp.MustCompile(`"parent_session_id"\s*:\s*"([^"]+)"`)
)

// Vertex spells a Messages call as .../publishers/anthropic/models/<model>:streamRawPredict
// (or :rawPredict when not streaming) and puts the model in the URL, not the body.
var vertexPathRe = regexp.MustCompile(`/publishers/anthropic/models/([^:/]+):(?:streamRawPredict|rawPredict)$`)

// VertexModel is the model a Vertex-shaped request path names, "" when the path
// is not a Vertex Messages call. "count-tokens" is Vertex's count_tokens
// endpoint wearing a model's name — callers that skip count_tokens skip it too.
func VertexModel(path string) string {
	if m := vertexPathRe.FindStringSubmatch(path); m != nil {
		return m[1]
	}
	return ""
}

const labelMax = 64

// ParseRequest extracts the fact-row values from a verbatim request body.
func ParseRequest(body []byte) (RequestFacts, error) {
	var req request
	if err := json.Unmarshal(body, &req); err != nil {
		return RequestFacts{}, fmt.Errorf("request json: %w", err)
	}

	f := RequestFacts{
		Model:      req.Model,
		Stream:     req.Stream,
		MaxTokens:  req.MaxTokens,
		Turns:      len(req.Messages),
		ToolCount:  len(req.Tools),
		TotalBytes: len(body),
	}
	if m := sessionRe.FindStringSubmatch(req.Metadata.UserID); m != nil {
		f.SessionID = m[1]
	}
	if m := parentSessionRe.FindStringSubmatch(req.Metadata.UserID); m != nil {
		f.ParentSessionID = m[1]
	}

	for _, t := range req.Tools {
		f.ToolsBytes += len(t)
	}
	for _, s := range systemBlocks(req.System) {
		f.SystemBytes += len(s)
	}
	for _, m := range req.Messages {
		f.MessagesBytes += len(m)
	}
	f.FirstText = firstText(req.Messages)
	f.Label = truncate(f.FirstText, labelMax)
	return f, nil
}

// systemBlocks splits the system section into the raw slices we measure. A
// string system is one "block" — its raw (quoted) bytes.
func systemBlocks(raw json.RawMessage) []json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	if isArray(raw) {
		var blocks []json.RawMessage
		if err := json.Unmarshal(raw, &blocks); err != nil {
			return nil
		}
		return blocks
	}
	return []json.RawMessage{raw}
}

// firstText is the first thing the *user* actually said, whole. The label is
// its first 64 chars.
//
// Two things pretend to be the user and are not. Claude Code injects
// system-reminders as whole messages with role "system" mid-conversation, and
// also as text blocks *inside* the user's own message — in the real fixture the
// first user message opens with a 13.7 KB reminder and the human's actual words
// ("hello world!") are the second block. Both are skipped, or every row in the
// dashboard would be labelled "<system-reminder>".
func firstText(msgs []json.RawMessage) string {
	for _, raw := range msgs {
		var m message
		if err := json.Unmarshal(raw, &m); err != nil || m.Role != "user" {
			continue
		}
		if txt := firstUserText(m.Content); txt != "" {
			return txt
		}
	}
	return ""
}

// LatestUserText returns the newest human-authored text in a captured request.
// A request carries the whole conversation, so this is the prompt that belongs
// to THIS exchange; FirstText is deliberately different and remains the stable
// label used to name the session.
func LatestUserText(body []byte) string {
	var req request
	if json.Unmarshal(body, &req) != nil {
		return ""
	}
	m, ok := latestMessage(req.Messages)
	if !ok || m.Role != "user" {
		return ""
	}
	return firstUserText(m.Content)
}

// latestMessage is the current hand-off in a request that carries a cumulative
// conversation. Looking farther back would turn a tool-result continuation
// into a fake repeat of the user prompt that preceded it.
func latestMessage(msgs []json.RawMessage) (message, bool) {
	if len(msgs) == 0 {
		return message{}, false
	}
	var m message
	if json.Unmarshal(msgs[len(msgs)-1], &m) != nil {
		return message{}, false
	}
	return m, true
}

// firstUserText pulls the first human-authored text out of a content section in
// either of the two forms the API accepts.
func firstUserText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	if !isArray(content) {
		var s string
		if err := json.Unmarshal(content, &s); err != nil || isSystemReminder(s) {
			return ""
		}
		return s
	}
	var blocks []block
	if err := json.Unmarshal(content, &blocks); err != nil {
		return ""
	}
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" && !isSystemReminder(b.Text) {
			return b.Text
		}
	}
	return ""
}

func isSystemReminder(s string) bool {
	return strings.HasPrefix(strings.TrimSpace(s), "<system-reminder>")
}

func truncate(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ") // a label is one line, always
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
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

// ---------------------------------------------------------------------------
// Breakdown — read-time itemization of a captured request
// ---------------------------------------------------------------------------

// Breakdown is the answer to "where did the money go in THIS request". Its json
// tags are the /api/capture contract. Not persisted: it is a pure interpretation
// of the capture blob, and it dies with it.
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
	CacheControl string `json:"cacheControl"` // "ephemeral/1h", "ephemeral", or ""
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

// BreakdownRequest itemizes a captured request body. It shares ParseRequest's
// marshaling rule (raw slices), so section sums equal the fact row's byte
// columns — a tested invariant, and the reason the inspector can be trusted.
func BreakdownRequest(body []byte) (Breakdown, error) {
	var req request
	if err := json.Unmarshal(body, &req); err != nil {
		return Breakdown{}, fmt.Errorf("request json: %w", err)
	}

	b := Breakdown{
		Tools:    make([]ToolItem, 0, len(req.Tools)),
		System:   make([]SystemItem, 0),
		Messages: make([]MessageItem, 0, len(req.Messages)),
		Flags: Flags{
			Thinking:          len(req.Thinking) > 0,
			ContextManagement: len(req.ContextManagement) > 0,
			OutputConfig:      len(req.OutputConfig) > 0,
		},
	}

	for _, raw := range req.Tools {
		var t tool
		json.Unmarshal(raw, &t) // a nameless tool still counts its bytes
		b.Tools = append(b.Tools, ToolItem{Name: t.Name, Bytes: len(raw)})
	}
	// Biggest schema first: the story this tab tells is "your tools are the bill".
	sort.SliceStable(b.Tools, func(i, j int) bool { return b.Tools[i].Bytes > b.Tools[j].Bytes })

	for _, raw := range systemBlocks(req.System) {
		var blk block
		json.Unmarshal(raw, &blk)
		b.System = append(b.System, SystemItem{Bytes: len(raw), CacheControl: cacheControl(blk.CacheControl)})
	}

	for _, raw := range req.Messages {
		var m message
		json.Unmarshal(raw, &m)
		b.Messages = append(b.Messages, MessageItem{
			Role:       m.Role,
			Bytes:      len(raw),
			BlockKinds: blockKinds(m.Content),
		})
	}
	return b, nil
}

// cacheControl renders a cache breakpoint as "ephemeral/1h". Breakpoints appear
// on system blocks and inside message blocks — never hard-code where.
func cacheControl(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var cc struct {
		Type string `json:"type"`
		TTL  string `json:"ttl"`
	}
	if err := json.Unmarshal(raw, &cc); err != nil || cc.Type == "" {
		return ""
	}
	if cc.TTL == "" {
		return cc.Type
	}
	return cc.Type + "/" + cc.TTL
}

func blockKinds(content json.RawMessage) []string {
	if len(content) == 0 {
		return []string{}
	}
	if !isArray(content) {
		return []string{"text"} // a bare string content is a text block
	}
	var blocks []block
	if err := json.Unmarshal(content, &blocks); err != nil {
		return []string{}
	}
	kinds := make([]string, 0, len(blocks))
	for _, b := range blocks {
		kinds = append(kinds, b.Type)
	}
	return kinds
}

// ---------------------------------------------------------------------------
// Response side
// ---------------------------------------------------------------------------

// Usage is the billed truth, verbatim from the API. Never priced here.
type Usage struct {
	In        int64 `json:"input_tokens"`
	Out       int64 `json:"output_tokens"`
	CacheRead int64 `json:"cache_read_input_tokens"`
	W5m       int64 `json:"cache_creation_5m_input_tokens"`
	W1h       int64 `json:"cache_creation_1h_input_tokens"`
}

// Block is one assembled content block, complete. An earlier capture format
// kept only the *type* of a thinking block and stringified tool inputs — and so
// could never answer a question it hadn't already thought of. This one keeps it all.
type Block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
}

// Response is the assembled message — what gets gzipped into response_gz. The
// blob alone must be able to answer any future question about the exchange.
type Response struct {
	ID         string  `json:"id,omitempty"`
	Model      string  `json:"model,omitempty"`
	Role       string  `json:"role,omitempty"`
	StopReason string  `json:"stop_reason,omitempty"`
	Usage      Usage   `json:"usage"`
	Content    []Block `json:"content"`

	// Set when the stream carried an error event; a non-2xx body is decoded by
	// DecodeError instead.
	ErrType string `json:"error_type,omitempty"`
	ErrMsg  string `json:"error_message,omitempty"`
}

// Op is the one-line display of what the assistant did.
func (r Response) Op() string {
	for _, b := range r.Content {
		if b.Type != "tool_use" {
			continue
		}
		op := "tool_use · " + b.Name
		if cmd := commandOf(b.Input); cmd != "" {
			op += " — " + truncate(cmd, 24)
			if len([]rune(cmd)) > 24 {
				op += "…"
			}
		}
		return op
	}
	return r.StopReason
}

// agentTools are the tool names a client uses to spawn a subagent. Claude Code
// has called it Task and Agent across versions; both take the subagent's whole
// opening message as a string field named "prompt".
var agentTools = map[string]bool{"Task": true, "Agent": true}

// AgentPrompts returns the prompt of every subagent this reply spawned.
//
// A subagent runs as its own API session with nothing on the wire naming its
// parent — the ONLY thread between the two is that the child's first user
// message is the prompt the parent's tool_use block carried. The recorder holds
// these prompts just long enough to recognize the child when it arrives.
func AgentPrompts(content []Block) []string {
	var out []string
	for _, b := range content {
		if b.Type != "tool_use" || !agentTools[b.Name] || len(b.Input) == 0 {
			continue
		}
		var in struct {
			Prompt string `json:"prompt"`
		}
		if json.Unmarshal(b.Input, &in) == nil && in.Prompt != "" {
			out = append(out, in.Prompt)
		}
	}
	return out
}

func commandOf(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var in struct {
		Command string `json:"command"`
	}
	json.Unmarshal(input, &in)
	return in.Command
}

// wireUsage decodes usage with pointers so a merge can be "later value wins per
// key": a key the event didn't send must not zero the value the last one did.
type wireUsage struct {
	In        *int64 `json:"input_tokens"`
	Out       *int64 `json:"output_tokens"`
	CacheRead *int64 `json:"cache_read_input_tokens"`

	// CacheWritten is the flat total of cache-creation tokens. Anthropic sends it
	// alongside the per-TTL split below; a gateway that rebuilds the response
	// sends it alone — LiteLLM's Anthropic endpoint emits exactly these two cache
	// keys and no breakdown, so without this field every write through a gateway
	// reads as zero and the request bills as if the prefix had been free.
	CacheWritten *int64 `json:"cache_creation_input_tokens"`

	CacheCreation *struct {
		W5m *int64 `json:"ephemeral_5m_input_tokens"`
		W1h *int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
}

// usageMerge folds the usage events of one response into a single Usage. It
// remembers whether the per-TTL breakdown has been seen, because the flat total
// is W5m+W1h: folding a later total into W5m would move 1-hour writes onto the
// 5-minute rate and underbill them. The breakdown wins wherever it exists, and
// the total only fills the gap it leaves.
type usageMerge struct {
	Usage
	split bool
}

func (m *usageMerge) merge(w wireUsage) {
	if w.In != nil {
		m.In = *w.In
	}
	if w.Out != nil {
		m.Out = *w.Out
	}
	if w.CacheRead != nil {
		m.CacheRead = *w.CacheRead
	}
	if w.CacheCreation != nil {
		m.split = true
		if w.CacheCreation.W5m != nil {
			m.W5m = *w.CacheCreation.W5m
		}
		if w.CacheCreation.W1h != nil {
			m.W1h = *w.CacheCreation.W1h
		}
		return
	}
	// No split available. A total on its own is a 5-minute write: it is the only
	// TTL a client gets without asking for the 1-hour beta, and reporting it as
	// nothing at all would be the one wrong answer.
	if w.CacheWritten != nil && !m.split {
		m.W5m, m.W1h = *w.CacheWritten, 0
	}
}

// sseEvent is the union of the event payloads we understand. Anything else in
// the stream is skipped.
type sseEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index"`

	Message *struct {
		ID    string    `json:"id"`
		Role  string    `json:"role"`
		Model string    `json:"model"`
		Usage wireUsage `json:"usage"`
	} `json:"message"`

	ContentBlock *struct {
		Type      string          `json:"type"`
		Text      string          `json:"text"`
		Thinking  string          `json:"thinking"`
		Signature string          `json:"signature"`
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Input     json.RawMessage `json:"input"`
	} `json:"content_block"`

	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`

	Usage *wireUsage `json:"usage"`

	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// maxSSELine bounds one event's data line. The proxy's tee caps the whole body
// at 8 MB, so nothing longer can reach us.
const maxSSELine = 8 << 20

// DecodeSSE assembles a streamed response. A truncated or partly-broken stream
// is not an error: we keep every block we understood and return it. Only a body
// that never even began a message is an error.
func DecodeSSE(body []byte) (Response, error) {
	var r Response
	r.Content = []Block{}

	// Blocks arrive by index and tool inputs arrive in fragments, so assemble
	// into an index-keyed scratch space and flatten at the end.
	type pending struct {
		Block
		inputJSON strings.Builder
	}
	blocks := map[int]*pending{}
	var order []int
	started := false
	var usage usageMerge

	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64<<10), maxSSELine)

	for sc.Scan() {
		line := sc.Bytes()
		// The payload carries its own "type", so the `event:` lines add nothing.
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(line[len("data:"):])
		if len(data) == 0 {
			continue
		}

		var ev sseEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			continue // truncated mid-event, or a shape we don't know: keep what we have
		}

		switch ev.Type {
		case "message_start":
			if ev.Message == nil {
				continue
			}
			started = true
			r.ID = ev.Message.ID
			r.Role = ev.Message.Role
			r.Model = ev.Message.Model
			usage.merge(ev.Message.Usage)

		case "content_block_start":
			if ev.ContentBlock == nil {
				continue
			}
			cb := ev.ContentBlock
			if _, dup := blocks[ev.Index]; !dup {
				order = append(order, ev.Index)
			}
			blocks[ev.Index] = &pending{Block: Block{
				Type:      cb.Type, // an unknown type is still recorded: we saw it
				Text:      cb.Text,
				Thinking:  cb.Thinking,
				Signature: cb.Signature,
				ID:        cb.ID,
				Name:      cb.Name,
			}}

		case "content_block_delta":
			p := blocks[ev.Index]
			if p == nil || ev.Delta == nil {
				continue
			}
			switch ev.Delta.Type {
			case "text_delta":
				p.Text += ev.Delta.Text
			case "thinking_delta":
				p.Thinking += ev.Delta.Thinking
			case "signature_delta":
				p.Signature += ev.Delta.Signature
			case "input_json_delta":
				p.inputJSON.WriteString(ev.Delta.PartialJSON)
			}

		case "content_block_stop":
			// Nothing to do: flattening handles the accumulated input.

		case "message_delta":
			if ev.Delta != nil && ev.Delta.StopReason != "" {
				r.StopReason = ev.Delta.StopReason
			}
			if ev.Usage != nil {
				usage.merge(*ev.Usage) // later wins, per key
			}

		case "error":
			if ev.Error != nil {
				r.ErrType, r.ErrMsg = ev.Error.Type, ev.Error.Message
			}
		}
	}

	r.Usage = usage.Usage

	for _, i := range order {
		p := blocks[i]
		b := p.Block
		if raw := p.inputJSON.String(); raw != "" {
			if json.Valid([]byte(raw)) {
				b.Input = json.RawMessage(raw)
			} else {
				// A tool call cut off mid-argument. Keep the fragment as a JSON
				// string rather than dropping it — it is evidence.
				b.Input, _ = json.Marshal(raw)
			}
		}
		r.Content = append(r.Content, b)
	}

	if !started && len(r.Content) == 0 && r.ErrType == "" {
		return r, errors.New("no message_start in stream")
	}
	return r, nil
}

// DecodeJSON decodes a non-streaming response.
func DecodeJSON(body []byte) (Response, error) {
	var m struct {
		ID         string    `json:"id"`
		Role       string    `json:"role"`
		Model      string    `json:"model"`
		StopReason string    `json:"stop_reason"`
		Usage      wireUsage `json:"usage"`
		Content    []Block   `json:"content"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return Response{}, fmt.Errorf("response json: %w", err)
	}
	if m.Model == "" && m.Content == nil {
		return Response{}, errors.New("response is not a message")
	}
	r := Response{
		ID:         m.ID,
		Role:       m.Role,
		Model:      m.Model,
		StopReason: m.StopReason,
		Content:    m.Content,
	}
	if r.Content == nil {
		r.Content = []Block{}
	}
	var usage usageMerge
	usage.merge(m.Usage)
	r.Usage = usage.Usage
	return r, nil
}

const errMsgMax = 200

// DecodeError extracts the error facts from a non-2xx body. An error body we
// cannot parse still produces a usable row: err_type becomes http_<status>.
func DecodeError(status int, body []byte) (errType, errMsg string) {
	var e struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &e); err == nil && e.Error.Type != "" {
		return e.Error.Type, truncate(e.Error.Message, errMsgMax)
	}
	return fmt.Sprintf("http_%d", status), truncate(string(body), errMsgMax)
}
