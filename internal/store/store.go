// Package store is the persistence layer: one SQLite file holding the facts
// (one row per recorded exchange) and, optionally, the captures (the verbatim
// bodies, gzipped) that those facts were read from.
//
// The split is the point. Facts are small, queryable, and permanent; captures
// are large, and the user can purge them (`DELETE FROM captures`) to reclaim
// disk without losing a single number. The foreign key runs captures →
// requests and never the other way, so that purge cannot cascade into the
// facts.
//
// Nothing here prices anything, and nothing here aggregates. Pricing is
// non-additive (a long-context tier that depends on per-request input size,
// and rates that depend on each row's timestamp), so a SUM in SQL would
// produce a wrong number that looks right. The read side hands whole rows to
// the api fold, which prices them one at a time.
package store

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"

	_ "modernc.org/sqlite"
)

// ErrNoCapture is returned by Capture when a request has no stored bodies —
// either they were purged, or the exchange never had a body worth keeping.
// The caller turns this into a 404; it is not a failure of the store.
var ErrNoCapture = errors.New("store: no capture for request")

// Row is one recorded exchange: the fact row.
//
// The usage columns are *int64 because "unknown" and "zero tokens" are
// different facts. A request whose body the parser choked on records no usage
// at all (err_type says why); a request that genuinely used no cache records
// zero. Collapsing those into 0 would silently invent spend data. Every other
// field is plain: it needs no parser — timing, status, byte sizes — so it is
// always known, even on the degradation ladder's bottom rung.
type Row struct {
	ID       int64  // set on read; ignored on insert
	TsMs     int64  // unix ms, arrival
	Endpoint string // "POST /v1/messages"

	SessionID   string // "" → NULL
	ModelReq    string
	ModelServed string // "" → NULL
	Status      int
	Streamed    bool
	Aborted     bool

	DurationMs int64
	TtftMs     int64

	StopReason string // "" → NULL
	Op         string // "" → NULL
	Label      string // "" → NULL

	InputTokens     *int64
	OutputTokens    *int64
	CacheReadTokens *int64
	CacheW5mTokens  *int64
	CacheW1hTokens  *int64

	Turns     int64
	ToolCount int64

	// The reply's shape: the billed output tokens apportioned across the block
	// types that produced them. An estimate — the API bills one output figure and
	// never says which block spent it — so it is split by block bytes at record
	// time, when the blocks are in hand.
	ThinkTokens int64
	TextTokens  int64
	ToolTokens  int64

	// Prefix is the request's cumulative cache-prefix hash chain, tools → system →
	// each message, as a JSON array. Prompt caching is a prefix match: the first
	// index at which two requests' chains differ IS what invalidated the cache, so
	// comparing a row against the one before it names the segment that broke it.
	// On the row rather than derived from the capture, because a capture can be
	// deleted and the diagnosis should outlive it.
	Prefix string

	// MaxTokens is the request's own output cap. 0 means we never learned it.
	// It is what tells a real request apart from a client's probe: nothing that
	// wants an answer asks for one token.
	MaxTokens int64

	TotalBytes    int64
	ToolsBytes    int64
	SystemBytes   int64
	MessagesBytes int64

	ErrType string // "" → NULL
	ErrMsg  string // "" → NULL
}

// UsageRow is the slim lifetime projection: enough to price a row, nothing
// more. Lifetime is re-read and re-priced on every dashboard poll, so it
// carries five integers and a model string instead of a whole Row.
//
// NULL usage reads back as 0 here — deliberately. Pricing an unknown token
// count as nothing is right; the fact that it was unknown is preserved on the
// Row, which is where anyone asking that question is already looking.
type UsageRow struct {
	TsMs int64

	// Both models, because they can differ and the difference is money: the API
	// bills what actually served the request, and an alias can be served by a
	// model we have no rate for. The fold decides which one to price; the store
	// just refuses to throw either away.
	ModelReq    string
	ModelServed string // "" when the response never told us

	In, Out, Read, W5m, W1h int64

	// Session identity, and just enough of the row to fold one. The sessions table
	// aggregates over every request ever recorded, not the last N, so it has to
	// ride on the one scan that already reads them all rather than pay for a
	// second. Op names the tool a turn called, which is how a session learns which
	// of its schemas it never used; MaxTokens and ToolCount tell a probe from a
	// failure; ToolsBytes keys the toolset cache that reads the schemas themselves.
	SessionID  string
	Status     int
	Label      string
	Op         string
	MaxTokens  int64
	ToolCount  int64
	ToolsBytes int64
}

// Store is a handle on the database file. It is safe for concurrent use: the
// recorder's single worker owns the writes, and the dashboard's read side
// shares this handle.
type Store struct {
	db *sql.DB
}

// migrations is the schema, in order. Each entry is applied exactly once and
// its index+1 is recorded in schema_migrations. Append to this slice; never
// edit an entry that has shipped.
var migrations = []string{
	// 1 — the two tables and their indexes.
	`
CREATE TABLE requests (
  id            INTEGER PRIMARY KEY,          -- rowid
  ts_ms         INTEGER NOT NULL,             -- unix ms, arrival
  endpoint      TEXT NOT NULL,                -- "POST /v1/messages"
  session_id    TEXT,                         -- NULL → 'unknown' at read time
  model_req     TEXT NOT NULL,
  model_served  TEXT,                         -- from message_start
  status        INTEGER NOT NULL,
  streamed      INTEGER NOT NULL,
  aborted       INTEGER NOT NULL DEFAULT 0,   -- client hung up; upstream drained, tokens still billed
  duration_ms   INTEGER,
  ttft_ms       INTEGER,                      -- arrival → first upstream body byte
  stop_reason   TEXT,
  op            TEXT,                         -- display: "tool_use · Bash — git st…"
  label         TEXT,                         -- first user text, ≤64 chars
  -- usage verbatim from the API (facts; never priced here)
  input_tokens INTEGER, output_tokens INTEGER,
  cache_read_tokens INTEGER, cache_w5m_tokens INTEGER, cache_w1h_tokens INTEGER,
  -- request shape (the composition facts behind "where money leaks")
  turns INTEGER, tool_count INTEGER,
  total_bytes INTEGER, tools_bytes INTEGER, system_bytes INTEGER, messages_bytes INTEGER,
  err_type TEXT, err_msg TEXT
);
CREATE INDEX idx_requests_ts      ON requests(ts_ms);
CREATE INDEX idx_requests_session ON requests(session_id, ts_ms);

CREATE TABLE captures (
  request_id  INTEGER PRIMARY KEY REFERENCES requests(id),
  request_gz  BLOB NOT NULL,                  -- verbatim request body, gzipped
  response_gz BLOB                            -- assembled response message JSON, gzipped
);
`,
	// 2 — max_tokens. A client's probe and a request that genuinely failed are
	// both a 4xx with no usage; this is the column that separates them. Claude
	// Code opens every session with a max_tokens:1 "quota" ping the API answers
	// with a 429 by design, and without this we count it as an error forever.
	// NULL on every pre-existing row: we cannot know, and will not guess.
	`ALTER TABLE requests ADD COLUMN max_tokens INTEGER;`,

	// 3 — the two facts the session trace cannot fold without: how the reply's
	// output tokens were spent (thinking vs text vs tool calls), and the cache
	// prefix chain that says which segment of a request broke the cache. Both are
	// derived from bodies we hold only at record time, and both must outlive the
	// capture they came from. NULL on every pre-existing row: those sessions draw
	// the panels they can and say nothing about the ones they cannot.
	`ALTER TABLE requests ADD COLUMN think_tokens INTEGER;
	 ALTER TABLE requests ADD COLUMN text_tokens INTEGER;
	 ALTER TABLE requests ADD COLUMN tool_tokens INTEGER;
	 ALTER TABLE requests ADD COLUMN prefix TEXT;`,
}

// Open opens (creating if needed) the database at path, applies the pragmas
// and any unapplied migrations, and returns a handle. It is idempotent:
// running it against an existing database applies nothing and errors on
// nothing.
func Open(path string) (*Store, error) {
	// The pragmas ride in the DSN so they are applied to every connection the
	// pool opens, not just the first. A pragma executed as a one-off statement
	// lands on whichever pooled connection happened to serve it and silently
	// does nothing for the others — the classic way to think you have WAL and
	// not have it.
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(ON)")
	dsn := "file:" + url.PathEscape(path) + "?" + q.Encode()

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}

	// One connection. Writes are already serialized by the recorder's single
	// worker, so this costs no write throughput, and it makes the whole class
	// of per-connection-pragma bugs impossible. The read side (the dashboard's
	// 2s poll) shares this connection, but cannot deadlock against a write:
	// every query here runs to completion and closes its rows before returning
	// — no nested queries, no long-lived cursors, no read held open across a
	// write.
	db.SetMaxOpenConns(1)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// migrate applies every migration the database has not seen, each in its own
// transaction alongside the row that records it — so a migration and the
// bookkeeping that says it ran commit together or not at all.
func (s *Store) migrate() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`)
	if err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}

	var applied int
	// NULL (no rows) → 0, so a fresh database starts below migration 1.
	if err := s.db.QueryRow(`SELECT coalesce(max(version), 0) FROM schema_migrations`).Scan(&applied); err != nil {
		return fmt.Errorf("store: read schema version: %w", err)
	}

	for i, stmt := range migrations {
		version := i + 1
		if version <= applied {
			continue
		}
		if err := s.applyMigration(version, stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) applyMigration(version int, stmt string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: migration %d: %w", version, err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(stmt); err != nil {
		return fmt.Errorf("store: migration %d: %w", version, err)
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES (?, unixepoch('subsec') * 1000)`, version); err != nil {
		return fmt.Errorf("store: migration %d: record version: %w", version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: migration %d: commit: %w", version, err)
	}
	return nil
}

// Close closes the database handle. main owns the handle; the recorder and the
// api read side only borrow it.
func (s *Store) Close() error {
	return s.db.Close()
}

const insertRequestSQL = `
INSERT INTO requests (
	ts_ms, endpoint, session_id, model_req, model_served, status, streamed, aborted,
	duration_ms, ttft_ms, stop_reason, op, label,
	input_tokens, output_tokens, cache_read_tokens, cache_w5m_tokens, cache_w1h_tokens,
	turns, tool_count, max_tokens, total_bytes, tools_bytes, system_bytes, messages_bytes,
	err_type, err_msg,
	think_tokens, text_tokens, tool_tokens, prefix
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// InsertExchange writes the fact row and, when there is a body worth keeping,
// the capture — in one transaction. There is never a capture without facts.
//
// reqBody and respBody are the verbatim bodies; the store gzips them. Bodies
// only, never headers: the API key lives in the headers, and it is not going
// in the database. respBody may be nil (the response never completed). If
// there is no body at all, the capture row is skipped and the fact row still
// lands.
//
// Returns the id of the new fact row.
func (s *Store) InsertExchange(r Row, reqBody, respBody []byte) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("store: insert: %w", err)
	}
	defer tx.Rollback() // no-op after a successful Commit

	res, err := tx.Exec(insertRequestSQL,
		r.TsMs, r.Endpoint, nullStr(r.SessionID), r.ModelReq, nullStr(r.ModelServed),
		r.Status, r.Streamed, r.Aborted,
		r.DurationMs, r.TtftMs, nullStr(r.StopReason), nullStr(r.Op), nullStr(r.Label),
		r.InputTokens, r.OutputTokens, r.CacheReadTokens, r.CacheW5mTokens, r.CacheW1hTokens,
		r.Turns, r.ToolCount, nullInt(r.MaxTokens), r.TotalBytes, r.ToolsBytes, r.SystemBytes, r.MessagesBytes,
		nullStr(r.ErrType), nullStr(r.ErrMsg),
		r.ThinkTokens, r.TextTokens, r.ToolTokens, nullStr(r.Prefix),
	)
	if err != nil {
		return 0, fmt.Errorf("store: insert request: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: insert request: %w", err)
	}

	if len(reqBody) > 0 || respBody != nil {
		reqGz, err := gz(reqBody)
		if err != nil {
			return 0, fmt.Errorf("store: gzip request body: %w", err)
		}
		// response_gz stays NULL when the response never completed — an absent
		// response and an empty one are different facts.
		var respGz any
		if respBody != nil {
			b, err := gz(respBody)
			if err != nil {
				return 0, fmt.Errorf("store: gzip response body: %w", err)
			}
			respGz = b
		}
		if _, err := tx.Exec(
			`INSERT INTO captures (request_id, request_gz, response_gz) VALUES (?, ?, ?)`,
			id, reqGz, respGz,
		); err != nil {
			return 0, fmt.Errorf("store: insert capture: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: commit: %w", err)
	}
	return id, nil
}

// Capture gunzips the stored bodies for a request. respJSON is nil when the
// response never completed. Returns ErrNoCapture when the row has no capture —
// purged, or never had one.
func (s *Store) Capture(id int64) (reqJSON, respJSON []byte, err error) {
	// response_gz is NULL-able; scanning it into a []byte leaves the slice nil,
	// which is exactly the distinction the caller needs.
	var reqGz, respGz []byte
	err = s.db.QueryRow(
		`SELECT request_gz, response_gz FROM captures WHERE request_id = ?`, id,
	).Scan(&reqGz, &respGz)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNoCapture
	}
	if err != nil {
		return nil, nil, fmt.Errorf("store: read capture %d: %w", id, err)
	}

	reqJSON, err = gunzip(reqGz)
	if err != nil {
		return nil, nil, fmt.Errorf("store: gunzip request %d: %w", id, err)
	}
	if respGz != nil {
		respJSON, err = gunzip(respGz)
		if err != nil {
			return nil, nil, fmt.Errorf("store: gunzip response %d: %w", id, err)
		}
	}
	return reqJSON, respJSON, nil
}

func gz(b []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(b); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func gunzip(b []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// nullStr maps "" to NULL. The Go side speaks in empty strings and the SQL
// side in NULLs; this is the only place that translation lives.
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullInt stores an unknown count as NULL rather than 0. A request that asks for
// zero output tokens does not exist, so 0 can only mean "we never parsed it" —
// and that is a different fact from any real cap.
func nullInt(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}
