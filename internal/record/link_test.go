package record

import (
	"fmt"
	"testing"
	"time"
)

// msgExchange is a minimal, complete, non-streamed exchange: one session, one
// opening user message, one assistant reply.
func msgExchange(sid, firstText, respJSON string) Exchange {
	// Claude Code packs a JSON document into the metadata.user_id *string*.
	userID := fmt.Sprintf(`{"session_id":%q}`, sid)
	req := fmt.Sprintf(`{"model":"claude-opus-4-8","max_tokens":4096,`+
		`"metadata":{"user_id":%q},`+
		`"messages":[{"role":"user","content":%q}]}`, userID, firstText)
	return Exchange{
		Start:    time.Now(),
		Method:   "POST",
		Path:     "/v1/messages",
		Status:   200,
		ReqBody:  []byte(req),
		RespBody: []byte(respJSON),
	}
}

func endTurn(text string) string {
	return fmt.Sprintf(`{"id":"m","role":"assistant","model":"claude-opus-4-8","stop_reason":"end_turn",`+
		`"usage":{"input_tokens":10,"output_tokens":5},"content":[{"type":"text","text":%q}]}`, text)
}

func spawnReply(tool, prompt string) string {
	return fmt.Sprintf(`{"id":"m","role":"assistant","model":"claude-opus-4-8","stop_reason":"tool_use",`+
		`"usage":{"input_tokens":10,"output_tokens":50},`+
		`"content":[{"type":"tool_use","id":"t1","name":%q,"input":{"subagent_type":"Explore","prompt":%q}}]}`, tool, prompt)
}

// A subagent runs as its own API session with nothing on the wire naming its
// parent; the one thread between them is the Task prompt becoming the child's
// first user message. The recorder must follow that thread and stamp
// parent_sid — this is the fact the whole "one row per session" grouping
// stands on.
func TestSubagentSessionLinksToItsParent(t *testing.T) {
	r, st := newRecorder(t)

	// The parent's turn spawns an agent…
	r.Record(msgExchange("parent-1", "refactor the billing tests", spawnReply("Task", "scan the repo for flaky tests")))
	// …the child arrives as its own session, opening with that prompt (the
	// client owns the incidental whitespace, so it must not matter)…
	r.Record(msgExchange("child-1", "  scan the repo for flaky tests\n", endTurn("found none")))
	// …and every later request of the child repeats the same first message.
	r.Record(msgExchange("child-1", "  scan the repo for flaky tests\n", endTurn("done")))
	// An unrelated session that opens with its own words stays unlinked.
	r.Record(msgExchange("other-1", "hello there", endTurn("hi")))

	rows := rowsOf(t, r, st) // newest first
	byOrder := map[string][]string{}
	for i := len(rows) - 1; i >= 0; i-- {
		byOrder[rows[i].SessionID] = append(byOrder[rows[i].SessionID], rows[i].ParentSid)
	}

	if got := byOrder["parent-1"]; len(got) != 1 || got[0] != "" {
		t.Errorf("parent rows ParentSid = %v, want one empty", got)
	}
	if got := byOrder["child-1"]; len(got) != 2 || got[0] != "parent-1" || got[1] != "parent-1" {
		t.Errorf("child rows ParentSid = %v, want [parent-1 parent-1]", got)
	}
	if got := byOrder["other-1"]; len(got) != 1 || got[0] != "" {
		t.Errorf("unrelated rows ParentSid = %v, want one empty", got)
	}
}

// The newer client spells the spawn tool "Agent"; the link must not care.
func TestSubagentLinkFollowsTheAgentSpelling(t *testing.T) {
	r, st := newRecorder(t)
	r.Record(msgExchange("p", "main work", spawnReply("Agent", "audit the proxy")))
	r.Record(msgExchange("c", "audit the proxy", endTurn("ok")))

	rows := rowsOf(t, r, st)
	for _, row := range rows {
		if row.SessionID == "c" && row.ParentSid != "p" {
			t.Errorf("child ParentSid = %q, want p", row.ParentSid)
		}
	}
}

// A session must never become its own parent, however its first message reads.
// A client echoing a spawn prompt back into the SAME session (a retry, a
// self-reference) is the degenerate case the sid guard exists for.
func TestSessionNeverLinksToItself(t *testing.T) {
	r, st := newRecorder(t)
	r.Record(msgExchange("s", "do it", spawnReply("Task", "do it")))
	r.Record(msgExchange("s", "do it", endTurn("done")))

	for _, row := range rowsOf(t, r, st) {
		if row.ParentSid != "" {
			t.Errorf("row of %q has ParentSid = %q, want none", row.SessionID, row.ParentSid)
		}
	}
}

// The spawn map is bounded FIFO: old prompts age out, new ones still match.
func TestLinkerEvictsOldestSpawns(t *testing.T) {
	l := newLinker()
	for i := 0; i < linkerCap+1; i++ {
		l.spawn(promptKey(fmt.Sprintf("prompt %d", i)), "p")
	}
	if _, ok := l.spawns[promptKey("prompt 0")]; ok {
		t.Error("oldest spawn survived past the cap")
	}
	if _, ok := l.spawns[promptKey(fmt.Sprintf("prompt %d", linkerCap))]; !ok {
		t.Error("newest spawn was evicted")
	}
}

// childExchange is msgExchange with the parent the client itself names. Claude
// Code puts parent_session_id in the same metadata.user_id blob as session_id.
func childExchange(sid, parent, firstText, respJSON string) Exchange {
	ex := msgExchange(sid, firstText, respJSON)
	userID := fmt.Sprintf(`{"session_id":%q,"parent_session_id":%q}`, sid, parent)
	ex.ReqBody = []byte(fmt.Sprintf(`{"model":"claude-opus-4-8","max_tokens":4096,`+
		`"metadata":{"user_id":%q},`+
		`"messages":[{"role":"user","content":%q}]}`, userID, firstText))
	return ex
}

// The link the client hands us, which the prompt match cannot reproduce. The
// child's opening message is the spawn prompt wrapped in an envelope — the real
// client's shape — so the hashes disagree and the fallback finds nothing. Only
// reading parent_session_id links these two, and this is the case the dashboard
// showing one row per session actually depends on.
func TestStatedParentLinksWhenThePromptDoesNotMatch(t *testing.T) {
	r, st := newRecorder(t)

	const prompt = "scan the repo for flaky tests"
	wrapped := "<teammate-message teammate_id=\"team-lead\">\n" + prompt + "\n</teammate-message>"

	r.Record(msgExchange("parent-1", "refactor the billing tests", spawnReply("Task", prompt)))
	r.Record(childExchange("child-1", "parent-1", wrapped, endTurn("found none")))

	rows := rowsOf(t, r, st)
	for _, row := range rows {
		if row.SessionID == "child-1" && row.ParentSid != "parent-1" {
			t.Errorf("child ParentSid = %q, want parent-1 — the stated parent was ignored", row.ParentSid)
		}
	}
}

// A client that names itself as its own parent must not be folded into its own
// row: that would count its spend twice and make the session its own subagent.
func TestStatedParentIgnoresSelfParenthood(t *testing.T) {
	r, st := newRecorder(t)
	r.Record(childExchange("solo-1", "solo-1", "hello there", endTurn("hi")))

	rows := rowsOf(t, r, st)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].ParentSid != "" {
		t.Errorf("ParentSid = %q, want empty", rows[0].ParentSid)
	}
}

// The stated parent takes precedence, but it must not switch off the fallback
// for everyone else: a stated child whose own reply spawns a grandchild still
// registers that prompt, so a client that names no parent still links.
func TestStatedParentStillRegistersItsOwnSpawns(t *testing.T) {
	r, st := newRecorder(t)

	r.Record(msgExchange("parent-1", "top level work", spawnReply("Task", "middle task")))
	// The middle session names its parent AND spawns a grandchild of its own.
	r.Record(childExchange("child-1", "parent-1", "middle task", spawnReply("Task", "leaf task")))
	// The grandchild names no parent — only the prompt match can place it.
	r.Record(msgExchange("grand-1", "leaf task", endTurn("done")))

	want := map[string]string{"parent-1": "", "child-1": "parent-1", "grand-1": "child-1"}
	for _, row := range rowsOf(t, r, st) {
		if w, ok := want[row.SessionID]; ok && row.ParentSid != w {
			t.Errorf("%s ParentSid = %q, want %q", row.SessionID, row.ParentSid, w)
		}
	}
}
