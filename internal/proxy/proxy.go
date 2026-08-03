// Package proxy is the client path: forward, stream, stamp, hand over. It does
// zero parsing — nothing here is allowed to make the user's stream feel slower.
package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/guydelarea/tokentracer/internal/record"
	"github.com/guydelarea/tokentracer/internal/wire"
)

const (
	// teeCap bounds what we keep, never what the client gets. 8 MB is far above
	// any real Messages response.
	teeCap = 8 << 20

	// drainCap bounds how long we keep reading a stream whose client already
	// left. The tokens are billed either way; we just refuse to wait forever.
	drainCap = 30 * time.Second

	copyChunk = 32 << 10
)

// Proxy forwards every request to the upstream API and hands the recordable
// ones to a Sink. It is the only component on the client path.
type Proxy struct {
	upstream *url.URL
	client   *http.Client
	upgrade  http.Handler
	sockets  *webSocketRegistry
	sink     record.Sink
}

func New(upstream string, sink record.Sink) (*Proxy, error) {
	u, err := url.Parse(upstream)
	if err != nil {
		return nil, fmt.Errorf("proxy: bad upstream %q: %w", upstream, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("proxy: upstream %q needs a scheme and host", upstream)
	}

	tr := http.DefaultTransport.(*http.Transport).Clone()
	// Compression off: an SSE stream we cannot read is an exchange we cannot
	// record. We strip the client's Accept-Encoding for the same reason.
	tr.DisableCompression = true

	sockets := newWebSocketRegistry()
	p := &Proxy{upstream: u, client: &http.Client{Transport: tr}, sockets: sockets, sink: sink}
	p.upgrade = &httputil.ReverseProxy{
		Director: func(r *http.Request) {
			path, rawPath := p.upstreamPaths(r)
			r.URL.Scheme = u.Scheme
			r.URL.Host = u.Host
			r.URL.Path = path
			r.URL.RawPath = rawPath
			r.Host = u.Host
			// Keeping frames uncompressed lets the recorder inspect their JSON
			// without changing what Codex or the Responses API sends.
			r.Header.Del("Sec-WebSocket-Extensions")
		},
		Transport: &webSocketTransport{base: tr, sink: sink, sockets: sockets},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			http.Error(w, "tokentracer: upstream unreachable: "+err.Error(), http.StatusBadGateway)
		},
	}

	// No client timeout — a long generation is not a hung request.
	return p, nil
}

// Close ends every upgraded connection. HTTP server shutdown does not own
// hijacked WebSockets, so callers must close the Proxy before closing its Sink.
func (p *Proxy) Close() { p.sockets.Close() }

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if websocketUpgrade(r.Header) {
		ctx := context.WithValue(r.Context(), webSocketPathKey{}, r.URL.Path)
		p.upgrade.ServeHTTP(w, r.WithContext(ctx))
		return
	}

	start := time.Now()

	reqBody, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		http.Error(w, "tokentracer: could not read request body", http.StatusBadRequest)
		return
	}

	// The upstream call must outlive the client. When a user hits esc, generated
	// tokens may still be billed, and final usage arrives after the client is
	// gone — so we detach from the client's cancellation.
	ctx := context.WithoutCancel(r.Context())
	out, err := http.NewRequestWithContext(ctx, r.Method, p.target(r), bytes.NewReader(reqBody))
	if err != nil {
		http.Error(w, "tokentracer: bad upstream request", http.StatusBadGateway)
		return
	}
	copyHeaders(out.Header, r.Header)
	out.Header.Del("Accept-Encoding") // identity, so the tee sees parseable bytes
	out.Host = p.upstream.Host

	resp, err := p.client.Do(out)
	if err != nil {
		// Nothing reached the API, so nothing was billed and there is no exchange
		// to record. ponytail: transport failures are the client's problem to see,
		// not a row; revisit if upstream flakiness ever needs a paper trail.
		http.Error(w, "tokentracer: upstream unreachable: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)

	// Watch for the client leaving even while we are blocked reading upstream,
	// so the drain deadline starts at the hangup and not at the next chunk.
	var aborted atomic.Bool
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-r.Context().Done():
			aborted.Store(true)
			deadline := time.AfterFunc(drainCap, func() { resp.Body.Close() })
			<-finished
			deadline.Stop()
		case <-finished:
		}
	}()

	var tee teeBuffer
	var ttft time.Duration
	buf := make([]byte, copyChunk)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if ttft == 0 {
				ttft = time.Since(start) // first upstream body byte — what the user feels
			}
			tee.Write(buf[:n])

			if !aborted.Load() {
				if _, werr := w.Write(buf[:n]); werr != nil {
					aborted.Store(true) // client is gone; keep draining upstream
				} else if flusher != nil {
					flusher.Flush() // per chunk: the whole point of the exercise
				}
			}
		}
		if rerr != nil {
			break // io.EOF, upstream error, or the drain deadline closing the body
		}
	}

	if p.sink == nil || !recordable(r) {
		return
	}
	p.sink.Record(record.Exchange{
		Start:         start,
		TTFT:          ttft,
		Duration:      time.Since(start),
		Method:        r.Method,
		Path:          r.URL.Path,
		Status:        resp.StatusCode,
		Streamed:      strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream"),
		ReqBody:       reqBody,
		RespBody:      tee.buf.Bytes(),
		RespTruncated: tee.truncated,
		ClientAborted: aborted.Load(),
	})
}

func websocketUpgrade(h http.Header) bool {
	if !strings.EqualFold(strings.TrimSpace(h.Get("Upgrade")), "websocket") {
		return false
	}
	for _, value := range h.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
				return true
			}
		}
	}
	return false
}

// recordable is the proxy's filter, and the Recorder's licence to assume every
// Exchange it receives becomes a row. count_tokens and everything else is
// proxied and forgotten — on Vertex, count_tokens hides behind the
// "count-tokens" pseudo-model.
func recordable(r *http.Request) bool {
	return wire.Recordable(r.Method, r.URL.Path)
}

// target is the upstream URL for a client request: the upstream base with the
// client's path and query appended.
//
// RawPath is set, not just Path, and that is load-bearing. url.String() re-escapes
// Path on Go's own terms, and Go escapes '[' and ']' — so a client asking for
// .../models/claude-opus-5[1m]:streamRawPredict left here as
// ...claude-opus-5%5B1m%5D... and Vertex 404'd a model that exists. On Vertex the
// model name IS the path, so re-escaping the path renames the model. RawPath
// carries the client's bytes through byte-for-byte; Go ignores it when it agrees
// with Path anyway, so the ordinary /v1/messages case is unaffected.
func (p *Proxy) target(r *http.Request) string {
	u := *p.upstream
	u.Path, u.RawPath = p.upstreamPaths(r)
	u.RawQuery = r.URL.RawQuery
	return u.String()
}

func (p *Proxy) upstreamPaths(r *http.Request) (path, rawPath string) {
	return appendUpstreamPath(p.upstream.Path, r.URL.Path),
		appendUpstreamPath(p.upstream.EscapedPath(), r.URL.EscapedPath())
}

func appendUpstreamPath(basePath, requestPath string) string {
	basePath = strings.TrimSuffix(basePath, "/")
	requestPath = dedupeCodexPath(basePath, requestPath)
	return basePath + requestPath
}

func dedupeCodexPath(basePath, requestPath string) string {
	if !strings.HasSuffix(basePath, "/codex") {
		return requestPath
	}
	if requestPath == "/codex" {
		return ""
	}
	if rest, ok := strings.CutPrefix(requestPath, "/codex/"); ok {
		return "/" + rest
	}
	return requestPath
}

// teeBuffer keeps the head of the stream and remembers that it gave up.
type teeBuffer struct {
	buf       bytes.Buffer
	truncated bool
}

func (t *teeBuffer) Write(p []byte) {
	room := teeCap - t.buf.Len()
	if room <= 0 {
		t.truncated = true
		return
	}
	if len(p) > room {
		p, t.truncated = p[:room], true
	}
	t.buf.Write(p)
}

// hopByHop headers belong to one connection and must not be forwarded.
var hopByHop = map[string]bool{
	"Connection":          true,
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Te":                  true,
	"Trailer":             true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
}

func copyHeaders(dst, src http.Header) {
	// Anything named in Connection is hop-by-hop for this message only.
	named := map[string]bool{}
	for _, v := range src.Values("Connection") {
		for _, f := range strings.Split(v, ",") {
			if f = strings.TrimSpace(f); f != "" {
				named[http.CanonicalHeaderKey(f)] = true
			}
		}
	}
	for k, vals := range src {
		if hopByHop[k] || named[k] || k == "Content-Length" {
			continue // Content-Length is the transport's to write, not ours to echo
		}
		for _, v := range vals {
			dst.Add(k, v)
		}
	}
}
