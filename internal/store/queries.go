package store

import (
	"database/sql"
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
	turns, tool_count, total_bytes, tools_bytes, system_bytes, messages_bytes,
	err_type, err_msg`

// Lifetime returns every row ever recorded, in the slim pricing projection,
// oldest first. The dashboard re-prices all of it on every poll — at v1 scale
// that is a few milliseconds, and it means the lifetime total can never drift
// from the rows it is derived from.
func (s *Store) Lifetime() ([]UsageRow, error) {
	rows, err := s.db.Query(`
		SELECT ts_ms, model_req, coalesce(model_served, ''),
		       coalesce(input_tokens, 0), coalesce(output_tokens, 0),
		       coalesce(cache_read_tokens, 0), coalesce(cache_w5m_tokens, 0), coalesce(cache_w1h_tokens, 0)
		FROM requests
		ORDER BY ts_ms`)
	if err != nil {
		return nil, fmt.Errorf("store: lifetime: %w", err)
	}
	defer rows.Close()

	var out []UsageRow
	for rows.Next() {
		var u UsageRow
		if err := rows.Scan(&u.TsMs, &u.ModelReq, &u.ModelServed, &u.In, &u.Out, &u.Read, &u.W5m, &u.W1h); err != nil {
			return nil, fmt.Errorf("store: lifetime: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
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
	var sessionID, modelServed, stopReason, op, label, errType, errMsg sql.NullString

	err := rows.Scan(
		&r.ID, &r.TsMs, &r.Endpoint, &sessionID, &r.ModelReq, &modelServed,
		&r.Status, &r.Streamed, &r.Aborted,
		&r.DurationMs, &r.TtftMs, &stopReason, &op, &label,
		&r.InputTokens, &r.OutputTokens, &r.CacheReadTokens, &r.CacheW5mTokens, &r.CacheW1hTokens,
		&r.Turns, &r.ToolCount, &r.TotalBytes, &r.ToolsBytes, &r.SystemBytes, &r.MessagesBytes,
		&errType, &errMsg,
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
	return r, nil
}
