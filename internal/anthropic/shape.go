package anthropic

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// The three derivations the session trace needs and the fact row cannot hold as
// a number: how the reply spent its output tokens, where a request's cached
// prefix diverged from the one before it, and what the session's tools have
// dumped back into its context.
//
// All three are computed from bodies, so they are computed once, at record time,
// while the body is in hand — with the exception of ResultsInContext, which reads
// the latest capture on demand: it describes the context as it stands now, not as
// it stood at any one request.

// ---------------------------------------------------------------------------
// Output shape
// ---------------------------------------------------------------------------

// SplitOutput apportions a reply's billed output tokens across the block types
// that produced them.
//
// It is the one estimate in the whole pipeline, and it is unavoidable: the API
// bills a single output_tokens figure and never says which block spent it. Block
// bytes are the only evidence available, so the split is proportional to them.
// The alternative — showing nothing — is worse, because "thinking is 60% of your
// output bill" is exactly the sort of thing you cannot see any other way.
//
// Bytes, not characters: a thinking block's signature is billed like everything
// else it carries.
func SplitOutput(out int64, blocks []Block) (think, text, tool int64) {
	var tb, xb, lb int64
	for _, b := range blocks {
		switch b.Type {
		case "thinking", "redacted_thinking":
			tb += int64(len(b.Thinking) + len(b.Signature))
		case "tool_use":
			lb += int64(len(b.Name) + len(b.Input))
		default:
			xb += int64(len(b.Text))
		}
	}
	total := tb + xb + lb
	if total == 0 || out <= 0 {
		return 0, 0, 0
	}
	think = out * tb / total
	tool = out * lb / total
	// Text takes the remainder rather than its own rounded share, so the three
	// always sum to exactly the figure the API billed. A split that does not add
	// up to the bill is a split nobody can trust.
	text = out - think - tool
	return think, text, tool
}

// ---------------------------------------------------------------------------
// Cache prefix
// ---------------------------------------------------------------------------

// PrefixHashes chains a hash over the request's cache prefix: tools, then
// system, then each message in turn.
//
// Prompt caching is a prefix match over exactly that sequence, and any byte
// change invalidates everything after it. Hashing the sequence *cumulatively*
// means two requests can be compared index by index, and the first index at
// which they differ is not a clue about what broke the cache — it IS what broke
// the cache. Index 0 is the toolset, index 1 the system prompt, and index 2+n
// message n.
//
// The raw wire bytes are hashed, not a re-encoding of them: the API's cache
// matches on what was actually sent, so a re-marshal that silently reordered a
// key would report an unbroken chain across a cache that really did break.
//
// ponytail: 64-bit hashes. A collision mislabels one comparison; widen if that
// ever costs more than the bytes on the row.
func PrefixHashes(body []byte) []string {
	var req request
	if err := json.Unmarshal(body, &req); err != nil {
		return nil
	}

	out := make([]string, 0, 2+len(req.Messages))
	var acc []byte // the running chain value
	step := func(segment []byte) {
		h := sha256.New()
		h.Write(acc)
		h.Write(segment)
		acc = h.Sum(nil)
		out = append(out, hex.EncodeToString(acc[:8]))
	}

	// The toolset is one segment: the cache does not break on tool 7 of 20, it
	// breaks on the tools block. Concatenating them is what makes index 0 mean
	// "your toolset changed".
	var tools []byte
	for _, t := range req.Tools {
		tools = append(tools, t...)
	}
	step(tools)

	var system []byte
	for _, s := range systemBlocks(req.System) {
		system = append(system, s...)
	}
	step(system)

	for _, m := range req.Messages {
		step(m)
	}
	return out
}

// FirstDiff is the index at which two prefix chains diverge — the segment that
// invalidated the cache. -1 when neither diverges from the other (identical, or
// one is a clean prefix of the other, which is the normal case: a conversation
// growing by a turn).
func FirstDiff(a, b []string) int {
	n := min(len(a), len(b))
	for i := range n {
		if a[i] != b[i] {
			return i
		}
	}
	return -1
}

// ---------------------------------------------------------------------------
// Tool results in context
// ---------------------------------------------------------------------------

// ResultItem is one tool's contribution to the tool_result bytes sitting in a
// request's context: how much that tool has returned, across how many calls.
//
// This is the number behind the staircase. Everything a tool returns stays in
// the message history and is re-read — and re-billed — by every request after
// it, so a tool that answered with 200KB an hour ago is still on the bill now.
type ResultItem struct {
	Name  string `json:"name"`
	Bytes int    `json:"bytes"`
	N     int    `json:"n"`
}

// ToolResultRef is one result the client fed back to the model. ToolUseID
// connects it to the response that invoked the tool without exposing its body.
type ToolResultRef struct {
	ToolUseID string `json:"toolUseId"`
	Bytes     int    `json:"bytes"`
}

// ResultsInContext groups the tool_result blocks in a captured request body by
// the tool that produced them, largest first.
//
// A tool_result names only the tool_use it answers, never the tool — so the
// tool_use ids are carried forward through the conversation and each result is
// attributed to the tool that produced it. Without that, the biggest blocks in
// a context are anonymous, and "which tool is flooding my context" has no answer.
func ResultsInContext(body []byte) []ResultItem {
	var req request
	if err := json.Unmarshal(body, &req); err != nil {
		return nil
	}

	names := map[string]string{} // tool_use id -> tool name
	by := map[string]*ResultItem{}

	for _, raw := range req.Messages {
		var m message
		if json.Unmarshal(raw, &m) != nil || !isArray(m.Content) {
			continue
		}
		var blocks []json.RawMessage
		if json.Unmarshal(m.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			var rb resultBlock
			if json.Unmarshal(b, &rb) != nil {
				continue
			}
			switch rb.Type {
			case "tool_use":
				if rb.ID != "" && rb.Name != "" {
					names[rb.ID] = rb.Name
				}
			case "tool_result":
				name := names[rb.ToolUseID]
				if name == "" {
					// The tool_use that produced it has been compacted out of the
					// history, but its answer is still here, still being re-read.
					name = "(compacted away)"
				}
				it := by[name]
				if it == nil {
					it = &ResultItem{Name: name}
					by[name] = it
				}
				it.Bytes += len(b)
				it.N++
			}
		}
	}

	out := make([]ResultItem, 0, len(by))
	for _, it := range by {
		out = append(out, *it)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Bytes > out[j].Bytes })
	return out
}

// ToolResults returns every tool result carried by one request. Unlike
// ResultsInContext it keeps individual hand-offs for the session-flow view.
func ToolResults(body []byte) []ToolResultRef {
	var req request
	if json.Unmarshal(body, &req) != nil {
		return nil
	}

	var out []ToolResultRef
	for _, raw := range req.Messages {
		var m message
		if json.Unmarshal(raw, &m) != nil || !isArray(m.Content) {
			continue
		}
		var blocks []json.RawMessage
		if json.Unmarshal(m.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			var rb resultBlock
			if json.Unmarshal(b, &rb) == nil && rb.Type == "tool_result" {
				out = append(out, ToolResultRef{ToolUseID: rb.ToolUseID, Bytes: len(b)})
			}
		}
	}
	return out
}

// resultBlock is the block shape the attribution needs: enough to link a
// tool_result back to the tool_use it answers.
type resultBlock struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	ToolUseID string `json:"tool_use_id"`
}
