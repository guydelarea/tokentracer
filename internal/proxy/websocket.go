package proxy

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/guydelarea/tokentracer/internal/record"
)

// webSocketTransport leaves the standard library's upgrade tunnel in charge
// of the connection and observes the framed messages crossing its upstream
// body. A 101 response body is an io.ReadWriteCloser: Read is server -> client,
// Write is client -> server.
type webSocketTransport struct {
	base    http.RoundTripper
	sink    record.Sink
	sockets *webSocketRegistry
}

func (t *webSocketTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(r)
	if err != nil || resp.StatusCode != http.StatusSwitchingProtocols {
		return resp, err
	}
	conn, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		return resp, nil
	}

	observer := &webSocketObserver{
		sink:     t.sink,
		path:     clientPath(r.URL.Path, r.Context().Value(webSocketPathKey{})),
		upstream: routeOf(r.Context()).Name,
	}
	connObserver := &observedWebSocket{
		ReadWriteCloser: conn,
		fromServer:      webSocketFrameDecoder{onMessage: observer.serverMessage},
		fromClient:      webSocketFrameDecoder{onMessage: observer.clientMessage},
		observer:        observer,
		sockets:         t.sockets,
	}
	resp.Body = connObserver
	if !t.sockets.Add(connObserver) {
		connObserver.Close()
	}
	return resp, nil
}

type webSocketPathKey struct{}

func clientPath(upstreamPath string, value any) string {
	if path, ok := value.(string); ok && path != "" {
		return path
	}
	return upstreamPath
}

type observedWebSocket struct {
	io.ReadWriteCloser
	fromServer webSocketFrameDecoder
	fromClient webSocketFrameDecoder
	observer   *webSocketObserver
	sockets    *webSocketRegistry
	observeMu  sync.Mutex
	closed     bool
	closeOnce  sync.Once
	closeErr   error
}

func (c *observedWebSocket) Read(p []byte) (int, error) {
	n, err := c.ReadWriteCloser.Read(p)
	if n > 0 {
		c.observeMu.Lock()
		if !c.closed {
			c.fromServer.Feed(p[:n])
		}
		c.observeMu.Unlock()
	}
	return n, err
}

func (c *observedWebSocket) Write(p []byte) (int, error) {
	// Observe before sending so an immediate upstream response cannot race ahead
	// of the response.create event that owns it.
	c.observeMu.Lock()
	if !c.closed {
		c.fromClient.Feed(p)
	}
	c.observeMu.Unlock()
	return c.ReadWriteCloser.Write(p)
}

func (c *observedWebSocket) Close() error {
	c.closeOnce.Do(func() {
		c.observeMu.Lock()
		c.closed = true
		c.observer.closeActive()
		c.observeMu.Unlock()
		c.sockets.Remove(c)
		c.closeErr = c.ReadWriteCloser.Close()
	})
	return c.closeErr
}

type webSocketRegistry struct {
	mu     sync.Mutex
	active map[*observedWebSocket]struct{}
	closed bool
}

func newWebSocketRegistry() *webSocketRegistry {
	return &webSocketRegistry{active: make(map[*observedWebSocket]struct{})}
}

func (r *webSocketRegistry) Add(conn *observedWebSocket) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false
	}
	r.active[conn] = struct{}{}
	return true
}

func (r *webSocketRegistry) Remove(conn *observedWebSocket) {
	r.mu.Lock()
	delete(r.active, conn)
	r.mu.Unlock()
}

func (r *webSocketRegistry) Close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	active := make([]*observedWebSocket, 0, len(r.active))
	for conn := range r.active {
		active = append(active, conn)
	}
	r.mu.Unlock()

	for _, conn := range active {
		conn.Close()
	}
}

type webSocketObserver struct {
	mu       sync.Mutex
	sink     record.Sink
	path     string
	upstream string // the route this socket was forwarded to
	active   *webSocketExchange
}

type webSocketExchange struct {
	start    time.Time
	ttft     time.Duration
	request  []byte
	response teeBuffer
}

func (o *webSocketObserver) clientMessage(opcode byte, payload []byte) {
	if opcode != 1 || webSocketEventType(payload) != "response.create" {
		return
	}

	now := time.Now()
	o.mu.Lock()
	previous := o.finishLocked(now, true)
	o.active = &webSocketExchange{start: now, request: append([]byte(nil), payload...)}
	o.mu.Unlock()
	o.record(previous)
}

func (o *webSocketObserver) serverMessage(opcode byte, payload []byte) {
	if opcode != 1 {
		return
	}

	now := time.Now()
	o.mu.Lock()
	if o.active == nil {
		o.mu.Unlock()
		return
	}
	if o.active.ttft == 0 {
		o.active.ttft = now.Sub(o.active.start)
	}
	o.active.response.Write([]byte("data: "))
	o.active.response.Write(payload)
	o.active.response.Write([]byte("\n\n"))

	var completed *record.Exchange
	switch webSocketEventType(payload) {
	case "response.completed", "response.failed", "response.incomplete", "error":
		completed = o.finishLocked(now, false)
	}
	o.mu.Unlock()
	o.record(completed)
}

func (o *webSocketObserver) closeActive() {
	o.mu.Lock()
	ex := o.finishLocked(time.Now(), true)
	o.mu.Unlock()
	o.record(ex)
}

func (o *webSocketObserver) finishLocked(now time.Time, aborted bool) *record.Exchange {
	if o.active == nil {
		return nil
	}
	active := o.active
	o.active = nil
	return &record.Exchange{
		Start:         active.start,
		TTFT:          active.ttft,
		Duration:      now.Sub(active.start),
		Method:        "WS",
		Path:          o.path,
		Upstream:      o.upstream,
		Status:        http.StatusSwitchingProtocols,
		Streamed:      true,
		ReqBody:       active.request,
		RespBody:      active.response.buf.Bytes(),
		RespTruncated: active.response.truncated,
		ClientAborted: aborted,
	}
}

func (o *webSocketObserver) record(ex *record.Exchange) {
	if ex != nil && o.sink != nil {
		o.sink.Record(*ex)
	}
}

func webSocketEventType(payload []byte) string {
	var event struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(payload, &event) != nil {
		return ""
	}
	return event.Type
}

type webSocketFrameDecoder struct {
	buf        []byte
	fragment   bytes.Buffer
	fragmentOp byte
	disabled   bool
	onMessage  func(byte, []byte)
}

func (d *webSocketFrameDecoder) Feed(chunk []byte) {
	if d.disabled || len(chunk) == 0 {
		return
	}
	d.buf = append(d.buf, chunk...)
	for d.nextFrame() {
	}
}

func (d *webSocketFrameDecoder) nextFrame() bool {
	if len(d.buf) < 2 {
		return false
	}
	fin := d.buf[0]&0x80 != 0
	opcode := d.buf[0] & 0x0f
	masked := d.buf[1]&0x80 != 0
	size := uint64(d.buf[1] & 0x7f)
	headerSize := 2
	switch size {
	case 126:
		if len(d.buf) < 4 {
			return false
		}
		size = uint64(binary.BigEndian.Uint16(d.buf[2:4]))
		headerSize = 4
	case 127:
		if len(d.buf) < 10 {
			return false
		}
		size = binary.BigEndian.Uint64(d.buf[2:10])
		headerSize = 10
	}
	if masked {
		headerSize += 4
	}
	if size > teeCap || uint64(headerSize)+size > uint64(^uint(0)>>1) {
		d.disabled = true
		d.buf = nil
		return false
	}
	frameSize := headerSize + int(size)
	if len(d.buf) < frameSize {
		return false
	}
	payload := append([]byte(nil), d.buf[headerSize:frameSize]...)
	if masked {
		mask := d.buf[headerSize-4 : headerSize]
		for i := range payload {
			payload[i] ^= mask[i%len(mask)]
		}
	}
	d.buf = d.buf[frameSize:]
	d.message(fin, opcode, payload)
	return len(d.buf) > 0
}

func (d *webSocketFrameDecoder) message(fin bool, opcode byte, payload []byte) {
	switch opcode {
	case 0:
		if d.fragmentOp == 0 || d.fragment.Len()+len(payload) > teeCap {
			d.fragment.Reset()
			d.fragmentOp = 0
			return
		}
		d.fragment.Write(payload)
		if fin {
			message := append([]byte(nil), d.fragment.Bytes()...)
			op := d.fragmentOp
			d.fragment.Reset()
			d.fragmentOp = 0
			d.onMessage(op, message)
		}
	case 1, 2:
		if fin {
			d.onMessage(opcode, payload)
			return
		}
		d.fragment.Reset()
		d.fragment.Write(payload)
		d.fragmentOp = opcode
	}
}
