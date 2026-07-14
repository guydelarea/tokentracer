package proxy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guydelarea/tokentracer/internal/record"
)

// fakeSink is the proxy's whole downstream contract: every Exchange handed over
// becomes a row. Tests read them off the channel.
type fakeSink struct {
	ch chan record.Exchange
}

func newFakeSink() *fakeSink { return &fakeSink{ch: make(chan record.Exchange, 8)} }

func (f *fakeSink) Record(ex record.Exchange) { f.ch <- ex }

// take waits for one Exchange; the proxy records post-stream, so the client can
// finish reading before the handler gets there.
func (f *fakeSink) take(t *testing.T) record.Exchange {
	t.Helper()
	select {
	case ex := <-f.ch:
		return ex
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for an Exchange")
		return record.Exchange{}
	}
}

func (f *fakeSink) none(t *testing.T) {
	t.Helper()
	select {
	case ex := <-f.ch:
		t.Fatalf("expected no Exchange, got one for %s %s", ex.Method, ex.Path)
	case <-time.After(200 * time.Millisecond):
	}
}

// newProxy wires a proxy in front of the given upstream handler.
func newProxy(t *testing.T, upstream http.Handler) (*httptest.Server, *fakeSink) {
	t.Helper()
	up := httptest.NewServer(upstream)
	t.Cleanup(up.Close)

	sink := newFakeSink()
	p, err := New(up.URL, sink)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	front := httptest.NewServer(p)
	t.Cleanup(front.Close)
	return front, sink
}

// The invariant the whole product rests on: the client sees each chunk as it is
// produced. Upstream refuses to send chunk 2 until the client has observed
// chunk 1 — so if the proxy buffers, this test deadlocks (and fails on timeout).
func TestNonBuffering(t *testing.T) {
	observed := make(chan struct{})

	front, _ := newProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		io.WriteString(w, "chunk-1\n")
		w.(http.Flusher).Flush()

		select {
		case <-observed:
		case <-time.After(5 * time.Second):
			t.Error("client never observed chunk 1 — the proxy is buffering")
			return
		}
		io.WriteString(w, "chunk-2\n")
		w.(http.Flusher).Flush()
	}))

	resp, err := http.Post(front.URL+"/v1/messages", "application/json", strings.NewReader(`{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	buf := make([]byte, len("chunk-1\n"))
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		t.Fatalf("reading chunk 1: %v", err)
	}
	if string(buf) != "chunk-1\n" {
		t.Fatalf("chunk 1 = %q", buf)
	}
	close(observed) // unblocks upstream's chunk 2

	rest, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(rest) != "chunk-2\n" {
		t.Fatalf("chunk 2 = %q", rest)
	}
}

// The proxy owns the record-or-not filter, so the Recorder may assume every
// Exchange it receives becomes a row.
func TestRecordFilter(t *testing.T) {
	front, sink := newProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ok":true}`)
	}))

	t.Run("POST /v1/messages is recorded", func(t *testing.T) {
		resp, err := http.Post(front.URL+"/v1/messages", "application/json", strings.NewReader(`{"model":"m"}`))
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		ex := sink.take(t)
		if ex.Method != "POST" || ex.Path != "/v1/messages" {
			t.Errorf("got %s %s", ex.Method, ex.Path)
		}
		if string(ex.ReqBody) != `{"model":"m"}` {
			t.Errorf("ReqBody = %q", ex.ReqBody)
		}
		if string(ex.RespBody) != `{"ok":true}` {
			t.Errorf("RespBody = %q", ex.RespBody)
		}
		if ex.Status != 200 {
			t.Errorf("Status = %d", ex.Status)
		}
		if ex.Duration <= 0 {
			t.Errorf("Duration = %v, want > 0", ex.Duration)
		}
		if ex.Start.IsZero() {
			t.Error("Start is zero")
		}
	})

	t.Run("count_tokens is proxied but never recorded", func(t *testing.T) {
		resp, err := http.Post(front.URL+"/v1/messages/count_tokens", "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(body) != `{"ok":true}` {
			t.Errorf("count_tokens was not proxied through: %q", body)
		}
		sink.none(t)
	})

	t.Run("Vertex Messages calls are recorded", func(t *testing.T) {
		path := "/v1/projects/p/locations/us-east5/publishers/anthropic/models/claude-sonnet-5:streamRawPredict"
		resp, err := http.Post(front.URL+path, "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		ex := sink.take(t)
		if ex.Path != path {
			t.Errorf("Path = %q", ex.Path)
		}
	})

	t.Run("Vertex count-tokens is proxied but never recorded", func(t *testing.T) {
		path := "/v1/projects/p/locations/us-east5/publishers/anthropic/models/count-tokens:rawPredict"
		resp, err := http.Post(front.URL+path, "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		sink.none(t)
	})

	t.Run("other paths are proxied but never recorded", func(t *testing.T) {
		resp, err := http.Get(front.URL + "/other")
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		sink.none(t)
	})
}

// An esc'd generation is billed spend. The client is gone, but the tokens were
// generated — so the proxy drains upstream to EOF and records the full body.
func TestAbortDrainsUpstream(t *testing.T) {
	clientGone := make(chan struct{})
	upstreamDone := make(chan struct{})

	front, sink := newProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(upstreamDone)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "before-abort\n")
		w.(http.Flusher).Flush()

		<-clientGone
		// The interesting bytes arrive after the client hung up: this is where
		// the final message_delta usage lives.
		io.WriteString(w, "after-abort\n")
		w.(http.Flusher).Flush()
	}))

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", front.URL+"/v1/messages", strings.NewReader(`{"model":"m"}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, len("before-abort\n"))
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		t.Fatalf("reading first chunk: %v", err)
	}
	cancel() // client hangs up mid-stream
	resp.Body.Close()

	// Give the proxy a moment to notice, then let upstream send the tail.
	time.Sleep(100 * time.Millisecond)
	close(clientGone)
	<-upstreamDone

	ex := sink.take(t)
	if !ex.ClientAborted {
		t.Error("ClientAborted = false, want true")
	}
	if !bytes.Contains(ex.RespBody, []byte("after-abort")) {
		t.Errorf("upstream was not drained after the client left: RespBody = %q", ex.RespBody)
	}
	if !bytes.Contains(ex.RespBody, []byte("before-abort")) {
		t.Errorf("pre-abort bytes lost: RespBody = %q", ex.RespBody)
	}
}

// The tee is capped so a runaway response can't eat the process. The client
// still gets every byte — the cap only bounds what we keep.
func TestTeeCapTruncates(t *testing.T) {
	const overflow = teeCap + 1<<20 // 1 MB past the cap

	front, sink := newProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := bytes.Repeat([]byte("x"), 1<<20)
		for written := 0; written < overflow; written += len(chunk) {
			w.Write(chunk)
		}
	}))

	resp, err := http.Post(front.URL+"/v1/messages", "application/json", strings.NewReader(`{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	n, err := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if n != overflow {
		t.Errorf("client received %d bytes, want the full %d — the cap must not truncate the client", n, overflow)
	}

	ex := sink.take(t)
	if !ex.RespTruncated {
		t.Error("RespTruncated = false, want true")
	}
	if len(ex.RespBody) != teeCap {
		t.Errorf("kept %d bytes, want the head capped at %d", len(ex.RespBody), teeCap)
	}
}

// Claude Code fires parallel requests routinely (subagents, background haiku
// calls). Two overlapping streams must produce two intact Exchanges.
func TestConcurrentStreams(t *testing.T) {
	front, sink := newProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		// Echo the request body back in two chunks, interleaving with the other
		// in-flight stream.
		io.WriteString(w, "start:")
		w.(http.Flusher).Flush()
		time.Sleep(50 * time.Millisecond)
		w.Write(body)
		w.(http.Flusher).Flush()
	}))

	var wg sync.WaitGroup
	for _, id := range []string{"alpha", "bravo"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			resp, err := http.Post(front.URL+"/v1/messages", "application/json", strings.NewReader(id))
			if err != nil {
				t.Error(err)
				return
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if want := "start:" + id; string(body) != want {
				t.Errorf("client %s got %q, want %q — streams crossed", id, body, want)
			}
		}(id)
	}
	wg.Wait()

	got := map[string]string{}
	for range 2 {
		ex := sink.take(t)
		got[string(ex.ReqBody)] = string(ex.RespBody)
	}
	for _, id := range []string{"alpha", "bravo"} {
		if want := "start:" + id; got[id] != want {
			t.Errorf("Exchange for %s: RespBody = %q, want %q", id, got[id], want)
		}
	}
}

func TestHopByHopHeadersStripped(t *testing.T) {
	seen := make(chan http.Header, 1)
	front, _ := newProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Clone()
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Keep-Alive", "timeout=5")
		w.Header().Set("X-Real-Header", "kept")
		io.WriteString(w, "ok")
	}))

	req, _ := http.NewRequest("POST", front.URL+"/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("X-Api-Key", "secret")
	req.Header.Set("Keep-Alive", "timeout=5")
	req.Header.Set("Proxy-Authorization", "Basic zzz")
	req.Header.Set("Te", "trailers")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	up := <-seen
	for _, h := range []string{"Keep-Alive", "Proxy-Authorization", "Te"} {
		if v := up.Get(h); v != "" {
			t.Errorf("hop-by-hop header %s reached upstream: %q", h, v)
		}
	}
	// End-to-end headers must survive — the API key is how the request authenticates.
	if got := up.Get("X-Api-Key"); got != "secret" {
		t.Errorf("X-Api-Key = %q, want it forwarded verbatim", got)
	}
	// Upstream compression is disabled so the tee sees parseable bytes.
	if got := up.Get("Accept-Encoding"); got != "" {
		t.Errorf("Accept-Encoding = %q, want it stripped so the recorded body is not gzip", got)
	}

	for _, h := range []string{"Connection", "Keep-Alive"} {
		if v := resp.Header.Get(h); v != "" {
			t.Errorf("hop-by-hop header %s reached the client: %q", h, v)
		}
	}
	if got := resp.Header.Get("X-Real-Header"); got != "kept" {
		t.Errorf("X-Real-Header = %q, want it passed through", got)
	}
}

// TTFT is stamped at the first upstream body byte, not at the response header —
// for a streamed answer those are far apart, and the gap is the number a user
// actually feels.
func TestTTFTStampedAtFirstBodyByte(t *testing.T) {
	front, sink := newProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush() // headers now, body later
		time.Sleep(150 * time.Millisecond)
		io.WriteString(w, "first-byte")
		w.(http.Flusher).Flush()
		time.Sleep(150 * time.Millisecond)
		io.WriteString(w, "-last")
	}))

	resp, err := http.Post(front.URL+"/v1/messages", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	ex := sink.take(t)
	if ex.TTFT < 100*time.Millisecond {
		t.Errorf("TTFT = %v, want ≥ ~150ms (stamped at first body byte)", ex.TTFT)
	}
	if ex.TTFT >= ex.Duration {
		t.Errorf("TTFT (%v) >= Duration (%v), want TTFT < Duration", ex.TTFT, ex.Duration)
	}
	if !ex.Streamed {
		t.Error("Streamed = false, want true for a text/event-stream response")
	}
}
