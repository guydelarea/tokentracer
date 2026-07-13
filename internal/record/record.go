// Package record owns one guarantee: no exchange the proxy hands over is ever
// lost. Not to a body we can't parse, not to a panic, not to a client that hung
// up, not to a failed insert. Something always lands, and it always says why.
package record

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/guydelarea/tokentracer/internal/anthropic"
	"github.com/guydelarea/tokentracer/internal/store"
)

// Exchange is one complete round-trip through the proxy: the raw material of a
// row. Nothing here is parsed — the Recorder does that off the client path.
type Exchange struct {
	Start    time.Time     // arrival
	TTFT     time.Duration // arrival → first upstream body byte
	Duration time.Duration // arrival → upstream EOF
	Method   string
	Path     string
	Status   int
	Streamed bool

	ReqBody  []byte // verbatim client request body
	RespBody []byte // upstream response bytes (SSE stream or JSON), as teed

	RespTruncated bool // response exceeded the tee cap; RespBody holds the head
	ClientAborted bool // client hung up mid-stream; upstream was drained anyway
}

// Sink receives every Exchange the proxy decides is recordable. The proxy owns
// that filter, so a Sink may assume each Exchange becomes a row.
type Sink interface {
	Record(Exchange)
}

// queueSize bounds the backlog. Full queue → Record blocks: backpressure, never
// loss. It is called post-stream, so the client never feels it.
const queueSize = 256

// retryDelay is the pause before the single insert retry — long enough for a
// transient lock to clear, short enough not to wedge the worker.
const retryDelay = 100 * time.Millisecond

// parseRequest is an internal seam. Tests swap it to inject a panic; nothing
// else may touch it.
var parseRequest = anthropic.ParseRequest

// Recorder turns Exchanges into rows. One worker goroutine does all the parsing
// and all the writing, which also serializes SQLite for free.
type Recorder struct {
	st    *store.Store
	queue chan Exchange
	done  chan struct{}
	once  sync.Once
}

// New starts the worker. It borrows the store: main owns the handle, and the
// dashboard's read side shares it.
func New(st *store.Store) *Recorder {
	r := &Recorder{
		st:    st,
		queue: make(chan Exchange, queueSize),
		done:  make(chan struct{}),
	}
	go r.work()
	return r
}

// Record hands an exchange to the worker, blocking if the queue is full.
//
// Calling Record after Close is an ordering violation, prevented by main's
// shutdown order (Server.Shutdown → Close) rather than by defensive code here.
func (r *Recorder) Record(ex Exchange) { r.queue <- ex }

// Close drains the queue unconditionally, then returns. The caller owns the
// deadline: the tail of a session is where the interesting requests are.
func (r *Recorder) Close() error {
	r.once.Do(func() { close(r.queue) })
	<-r.done
	return nil
}

func (r *Recorder) work() {
	defer close(r.done)
	for ex := range r.queue {
		r.handle(ex)
	}
}

// handle is the whole degradation ladder. It cannot fail: every path below ends
// in a row, and a panic anywhere still leaves the worker alive for the next one.
func (r *Recorder) handle(ex Exchange) {
	defer func() {
		if p := recover(); p != nil {
			// Nothing below is supposed to panic. If it does, we still refuse to
			// take the worker down with it.
			log.Printf("tokentracer: recovered from panic while recording %s %s: %v", ex.Method, ex.Path, p)
		}
	}()

	row, respJSON := build(ex)
	r.insert(row, ex.ReqBody, respJSON)
}

// err_type precedence: the upstream's own error outranks anything we concluded,
// because a fact outranks an interpretation. Our rungs only ever appear on an
// otherwise-successful exchange.
const (
	rungOversize = iota + 1 // we truncated the capture
	rungParse               // we could not understand the body
	rungPanic               // we have a bug; the capture is the repro case
	rungUpstream            // the API said no
)

// setErr keeps the highest-ranked explanation. Degradation is per side, so the
// message always names the broken side.
func setErr(row *store.Row, rank int, typ, msg string, best *int) {
	if rank < *best {
		return
	}
	*best, row.ErrType, row.ErrMsg = rank, typ, msg
}

// build turns an Exchange into a row plus the response blob to keep. Both sides
// degrade independently: a request body we can't parse never costs us the usage
// facts from a response we could.
func build(ex Exchange) (store.Row, []byte) {
	row := store.Row{
		TsMs:       ex.Start.UnixMilli(),
		Endpoint:   ex.Method + " " + ex.Path,
		Status:     ex.Status,
		Streamed:   ex.Streamed,
		Aborted:    ex.ClientAborted,
		DurationMs: ex.Duration.Milliseconds(),
		TtftMs:     ex.TTFT.Milliseconds(),
		// Facts that need no parser, and so survive every rung below.
		TotalBytes: int64(len(ex.ReqBody)),
	}
	best := 0

	if ex.RespTruncated {
		setErr(&row, rungOversize, "oversize", fmt.Sprintf("response: exceeded the capture cap; kept the first %d bytes", len(ex.RespBody)), &best)
	}

	buildRequest(&row, ex, &best)
	respJSON := buildResponse(&row, ex, &best)
	return row, respJSON
}

func buildRequest(row *store.Row, ex Exchange, best *int) {
	defer func() {
		if p := recover(); p != nil {
			setErr(row, rungPanic, "panic", fmt.Sprintf("request: panic: %v", p), best)
		}
	}()

	facts, err := parseRequest(ex.ReqBody)
	if err != nil {
		setErr(row, rungParse, "parse", "request: "+err.Error(), best)
		return
	}

	row.ModelReq = facts.Model
	row.SessionID = facts.SessionID
	row.Label = facts.Label
	row.Turns = int64(facts.Turns)
	row.ToolCount = int64(facts.ToolCount)
	row.MaxTokens = int64(facts.MaxTokens)
	row.ToolsBytes = int64(facts.ToolsBytes)
	row.SystemBytes = int64(facts.SystemBytes)
	row.MessagesBytes = int64(facts.MessagesBytes)
	// The proxy's Streamed came off the response content-type; the request's own
	// stream flag is the better fact when the response never arrived.
	if !row.Streamed {
		row.Streamed = facts.Stream
	}
}

// buildResponse fills the usage facts and returns the blob to store. On every
// failure path it returns the raw bytes instead: the capture is what lets us
// find out what went wrong.
func buildResponse(row *store.Row, ex Exchange, best *int) (respJSON []byte) {
	respJSON = ex.RespBody // the fallback on every rung below

	defer func() {
		if p := recover(); p != nil {
			setErr(row, rungPanic, "panic", fmt.Sprintf("response: panic: %v", p), best)
			respJSON = ex.RespBody
		}
	}()

	if ex.Status >= 400 {
		typ, msg := anthropic.DecodeError(ex.Status, ex.RespBody)
		setErr(row, rungUpstream, typ, "response: "+msg, best)
		return respJSON // keep the error body verbatim
	}

	var resp anthropic.Response
	var err error
	if ex.Streamed {
		resp, err = anthropic.DecodeSSE(ex.RespBody)
	} else {
		resp, err = anthropic.DecodeJSON(ex.RespBody)
	}
	if err != nil {
		setErr(row, rungParse, "parse", "response: "+err.Error(), best)
		return respJSON
	}

	row.ModelServed = resp.Model
	row.StopReason = resp.StopReason
	row.Op = resp.Op()
	row.InputTokens = ptr(resp.Usage.In)
	row.OutputTokens = ptr(resp.Usage.Out)
	row.CacheReadTokens = ptr(resp.Usage.CacheRead)
	row.CacheW5mTokens = ptr(resp.Usage.W5m)
	row.CacheW1hTokens = ptr(resp.Usage.W1h)

	// A 200 whose stream carried an error event is still the upstream saying no.
	if resp.ErrType != "" {
		setErr(row, rungUpstream, resp.ErrType, "response: "+resp.ErrMsg, best)
	}

	// Store the assembled message, not the raw SSE: it is ~3× smaller and it is
	// the only thing that can answer a question we haven't thought of yet.
	assembled, err := json.Marshal(resp)
	if err != nil {
		return ex.RespBody
	}
	return assembled
}

func (r *Recorder) insert(row store.Row, reqBody, respJSON []byte) {
	_, err := r.st.InsertExchange(row, reqBody, respJSON)
	if err == nil {
		return
	}

	// Ladder bottom. Retry once — a lock clears, a disk hiccup passes.
	time.Sleep(retryDelay)
	if _, err2 := r.st.InsertExchange(row, reqBody, respJSON); err2 == nil {
		return
	}

	// Then give up loudly, with the facts in the log, and stay alive. Never crash
	// (the proxy is live infrastructure mid-session) and never retry forever (a
	// wedged worker backs the queue up into Record).
	log.Printf("tokentracer: DROPPED exchange after retry: %v | ts=%d endpoint=%q model=%q status=%d in=%d out=%d duration=%dms",
		err, row.TsMs, row.Endpoint, row.ModelReq, row.Status, deref(row.InputTokens), deref(row.OutputTokens), row.DurationMs)
}

func ptr(v int64) *int64 { return &v }

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}
