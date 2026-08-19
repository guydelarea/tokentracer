package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// The read side is three targeted SELECTs and no aggregation whatsoever.
//
// That is not laziness, it is correctness. Pricing is non-additive: the
// long-context tier charges premium rates once a request's total input crosses
// a threshold, which is a per-request fact that `SUM(...) GROUP BY model`
// destroys — and rates are time-windowed, so each row must be priced at its
// own timestamp, not the group's. A SQL sum would be fast, plausible, and
// wrong. Rows come out whole; the api fold prices them one at a time.

// rowColumns is the column list every full-Row read shares, in the order
// scanRow expects. Keeping the two next to each other is what stops a column
// added to one and forgotten in the other.
const rowColumns = `
	id, ts_ms, endpoint, session_id, model_req, model_served, status, streamed, aborted,
	duration_ms, ttft_ms, stop_reason, op, label,
	input_tokens, output_tokens, cache_read_tokens, cache_w5m_tokens, cache_w1h_tokens,
	turns, tool_count, coalesce(max_tokens, 0), total_bytes, tools_bytes, system_bytes, messages_bytes,
	err_type, err_msg,
	coalesce(think_tokens, 0), coalesce(text_tokens, 0), coalesce(tool_tokens, 0), prefix, parent_sid, upstream`

// Lifetime returns every row ever recorded, in the slim pricing projection,
// oldest first. The dashboard re-prices all of it on every poll — at v1 scale
// that is a few milliseconds, and it means the lifetime total can never drift
// from the rows it is derived from.
// It carries the session columns too. The sessions table aggregates over every
// request ever recorded — a session that started this morning is still the one
// on screen — so it folds out of this same scan rather than paying for a second
// one. Six cheap columns on a pass that was already reading the row.
func (s *Store) Lifetime() ([]UsageRow, error) {
	rows, err := s.db.Query(`
		SELECT ts_ms, model_req, coalesce(model_served, ''),
		       coalesce(input_tokens, 0), coalesce(output_tokens, 0),
		       coalesce(cache_read_tokens, 0), coalesce(cache_w5m_tokens, 0), coalesce(cache_w1h_tokens, 0),
		       coalesce(session_id, ''), coalesce(parent_sid, ''), status, coalesce(label, ''), coalesce(op, ''),
		       coalesce(max_tokens, 0), tool_count, tools_bytes, coalesce(upstream, '')
		FROM requests
		ORDER BY ts_ms`)
	if err != nil {
		return nil, fmt.Errorf("store: lifetime: %w", err)
	}
	defer rows.Close()

	var out []UsageRow
	for rows.Next() {
		var u UsageRow
		if err := rows.Scan(&u.TsMs, &u.ModelReq, &u.ModelServed, &u.In, &u.Out, &u.Read, &u.W5m, &u.W1h,
			&u.SessionID, &u.ParentSid, &u.Status, &u.Label, &u.Op, &u.MaxTokens, &u.ToolCount, &u.ToolsBytes,
			&u.Upstream); err != nil {
			return nil, fmt.Errorf("store: lifetime: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// Session returns every row of one session, oldest first — the grain the trace
// folds over. Chronological because every cache event is defined against the
// request before it: a re-written prefix is only a *break* if the one before it
// primed the cache.
func (s *Store) Session(sid string) ([]Row, error) {
	rows, err := s.db.Query(
		`SELECT`+rowColumns+` FROM requests WHERE coalesce(session_id, '') = ? ORDER BY ts_ms, id`, sid)
	if err != nil {
		return nil, fmt.Errorf("store: session: %w", err)
	}
	defer rows.Close()
	return scanRows(rows, "session")
}

// AgentRows returns every row of every subagent session a parent spawned,
// oldest first. Grouping the rows by their own session_id is the caller's job —
// one query serves however many agents the session ran.
func (s *Store) AgentRows(parentSid string) ([]Row, error) {
	rows, err := s.db.Query(
		`SELECT`+rowColumns+` FROM requests WHERE parent_sid = ? ORDER BY ts_ms, id`, parentSid)
	if err != nil {
		return nil, fmt.Errorf("store: agent rows: %w", err)
	}
	defer rows.Close()
	return scanRows(rows, "agent rows")
}

// LatestToolsCapture is the id of the newest request in a session that shipped
// tool schemas — the capture the cut list reads the schemas themselves out of.
// The newest, because a session's toolset is whatever it is shipping *now*: a
// tool dropped from the request an hour ago is not one you can still cut.
func (s *Store) LatestToolsCapture(sid string) (int64, error) {
	var id int64
	err := s.db.QueryRow(
		`SELECT id FROM requests
		 WHERE coalesce(session_id, '') = ? AND tool_count > 0
		 ORDER BY ts_ms DESC, id DESC LIMIT 1`, sid).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNoCapture
	}
	if err != nil {
		return 0, fmt.Errorf("store: latest tools capture: %w", err)
	}
	return id, nil
}

// Window returns the full rows recorded at or after since, oldest first. The
// boundary is inclusive: a row stamped exactly at since is in the window.
func (s *Store) Window(since time.Time) ([]Row, error) {
	rows, err := s.db.Query(
		`SELECT`+rowColumns+` FROM requests WHERE ts_ms >= ? ORDER BY ts_ms`,
		since.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("store: window: %w", err)
	}
	defer rows.Close()
	return scanRows(rows, "window")
}

// Recent returns the newest n rows, newest first — the request log.
func (s *Store) Recent(n int) ([]Row, error) {
	rows, err := s.db.Query(
		`SELECT`+rowColumns+` FROM requests ORDER BY id DESC LIMIT ?`, n)
	if err != nil {
		return nil, fmt.Errorf("store: recent: %w", err)
	}
	defer rows.Close()
	return scanRows(rows, "recent")
}

func scanRows(rows *sql.Rows, what string) ([]Row, error) {
	var out []Row
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, fmt.Errorf("store: %s: %w", what, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: %s: %w", what, err)
	}
	return out, nil
}

// scanRow reads one fact row. The nullable text columns come back as
// sql.NullString and flatten to "" — the Go side has no use for a third state
// beyond "absent". The usage columns keep theirs: a nil *int64 means the token
// count was never learned, which is a different fact from zero tokens.
func scanRow(rows *sql.Rows) (Row, error) {
	var r Row
	var sessionID, modelServed, stopReason, op, label, errType, errMsg, prefix, parentSid, upstream sql.NullString

	err := rows.Scan(
		&r.ID, &r.TsMs, &r.Endpoint, &sessionID, &r.ModelReq, &modelServed,
		&r.Status, &r.Streamed, &r.Aborted,
		&r.DurationMs, &r.TtftMs, &stopReason, &op, &label,
		&r.InputTokens, &r.OutputTokens, &r.CacheReadTokens, &r.CacheW5mTokens, &r.CacheW1hTokens,
		&r.Turns, &r.ToolCount, &r.MaxTokens, &r.TotalBytes, &r.ToolsBytes, &r.SystemBytes, &r.MessagesBytes,
		&errType, &errMsg,
		&r.ThinkTokens, &r.TextTokens, &r.ToolTokens, &prefix, &parentSid, &upstream,
	)
	if err != nil {
		return Row{}, err
	}

	r.SessionID = sessionID.String
	r.ModelServed = modelServed.String
	r.StopReason = stopReason.String
	r.Op = op.String
	r.Label = label.String
	r.ErrType = errType.String
	r.ErrMsg = errMsg.String
	r.Prefix = prefix.String
	r.ParentSid = parentSid.String
	r.Upstream = upstream.String
	return r, nil
}
