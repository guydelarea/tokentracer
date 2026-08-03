package proxy

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guydelarea/tokentracer/internal/record"
	"github.com/guydelarea/tokentracer/internal/store"
)

const testWebSocketKey = "dGhlIHNhbXBsZSBub25jZQ=="

func TestWebSocketResponsesAreBidirectional(t *testing.T) {
	const request = `{"type":"response.create","model":"gpt-5.6","prompt_cache_key":"codex-session","input":"hello"}`
	const response = `{"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"gpt-5.6","output":[],"usage":{"input_tokens":10,"output_tokens":2}}}`

	upstreamRequest := make(chan string, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RequestURI() != "/backend-api/codex/responses?trace=1" {
			t.Errorf("upstream URI = %q", r.URL.RequestURI())
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusMethodNotAllowed)
			io.WriteString(w, `{"detail":"Method Not Allowed"}`)
			return
		}

		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack upstream: %v", err)
			return
		}
		defer conn.Close()
		fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: %s\r\n\r\n", websocketAccept(r.Header.Get("Sec-WebSocket-Key")))
		if err := rw.Flush(); err != nil {
			t.Errorf("flush upstream handshake: %v", err)
			return
		}

		opcode, payload, err := readWebSocketFrame(rw)
		if err != nil {
			t.Errorf("read client frame: %v", err)
			return
		}
		if opcode != 1 {
			t.Errorf("client opcode = %d, want text", opcode)
		}
		upstreamRequest <- string(payload)
		if err := writeWebSocketFrame(rw, false, 1, []byte(response)); err != nil {
			t.Errorf("write server frame: %v", err)
			return
		}
		if err := rw.Flush(); err != nil {
			t.Errorf("flush server frame: %v", err)
		}
	}))
	t.Cleanup(up.Close)

	p, err := New(up.URL+"/backend-api/codex", nil)
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(p)
	t.Cleanup(front.Close)

	conn, reader, err := openTestWebSocket(front.URL+"/responses?trace=1", http.Header{
		"Authorization": []string{"Bearer test-token"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if err := writeWebSocketFrame(conn, true, 1, []byte(request)); err != nil {
		t.Fatal(err)
	}
	if got := <-upstreamRequest; got != request {
		t.Errorf("upstream request = %s", got)
	}
	opcode, payload, err := readWebSocketFrame(reader)
	if err != nil {
		t.Fatal(err)
	}
	if opcode != 1 || string(payload) != response {
		t.Errorf("client frame = opcode %d, %s", opcode, payload)
	}
}

func TestWebSocketResponsesAreRecorded(t *testing.T) {
	const request = `{"type":"response.create","model":"gpt-5.6","prompt_cache_key":"codex-session","input":"hello"}`
	const created = `{"type":"response.created","response":{"id":"resp_1","status":"in_progress","model":"gpt-5.6","output":[]}}`
	const completed = `{"type":"response.completed","response":{"id":"resp_1","status":"completed","model":"gpt-5.6","output":[],"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":4},"output_tokens":2}}}`

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack upstream: %v", err)
			return
		}
		defer conn.Close()
		fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: %s\r\n\r\n", websocketAccept(r.Header.Get("Sec-WebSocket-Key")))
		if err := rw.Flush(); err != nil {
			t.Errorf("flush upstream handshake: %v", err)
			return
		}
		if _, _, err := readWebSocketFrame(rw); err != nil {
			t.Errorf("read client frame: %v", err)
			return
		}
		for _, event := range []string{created, completed} {
			if err := writeWebSocketFrame(rw, false, 1, []byte(event)); err != nil {
				t.Errorf("write server frame: %v", err)
				return
			}
			if err := rw.Flush(); err != nil {
				t.Errorf("flush server frame: %v", err)
				return
			}
		}
		<-r.Context().Done()
	}))
	t.Cleanup(up.Close)

	st, err := store.Open(filepath.Join(t.TempDir(), "websocket.db"))
	if err != nil {
		t.Fatal(err)
	}
	recorder := record.New(st)
	t.Cleanup(func() {
		recorder.Close()
		st.Close()
	})
	sink := newFakeSink()
	p, err := New(up.URL, fanoutSink{recorder, sink})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(p)
	t.Cleanup(front.Close)

	conn, reader, err := openTestWebSocket(front.URL+"/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := writeWebSocketFrame(conn, true, 1, []byte(request)); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, _, err := readWebSocketFrame(reader); err != nil {
			t.Fatal(err)
		}
	}

	select {
	case ex := <-sink.ch:
		if ex.Method != "WS" || ex.Path != "/responses" || ex.Status != http.StatusSwitchingProtocols || !ex.Streamed {
			t.Errorf("exchange route facts = %+v", ex)
		}
		if string(ex.ReqBody) != request {
			t.Errorf("request capture = %s", ex.ReqBody)
		}
		wantResponse := "data: " + created + "\n\ndata: " + completed + "\n\n"
		if string(ex.RespBody) != wantResponse {
			t.Errorf("response capture = %s", ex.RespBody)
		}
		if ex.TTFT <= 0 || ex.Duration < ex.TTFT || ex.RespTruncated || ex.ClientAborted {
			t.Errorf("exchange timing/degradation facts = %+v", ex)
		}
	case <-time.After(time.Second):
		t.Fatal("WebSocket response was forwarded but not recorded")
	}

	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	rows, err := st.Recent(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("stored rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.Endpoint != "WS /responses" || row.Status != http.StatusSwitchingProtocols || !row.Streamed {
		t.Errorf("stored route facts = %+v", row)
	}
	if row.SessionID != "codex-session" || row.ModelReq != "gpt-5.6" || row.ModelServed != "gpt-5.6" {
		t.Errorf("stored identity facts = %+v", row)
	}
	if row.InputTokens == nil || *row.InputTokens != 6 || row.CacheReadTokens == nil || *row.CacheReadTokens != 4 ||
		row.OutputTokens == nil || *row.OutputTokens != 2 || row.ErrType != "" {
		t.Errorf("stored usage facts = %+v", row)
	}
}

func TestProxyCloseClosesWebSocketsAndFlushesActiveExchange(t *testing.T) {
	const request = `{"type":"response.create","model":"gpt-5.6","prompt_cache_key":"closing-session","input":"hello"}`
	const created = `{"type":"response.created","response":{"id":"resp_closing","status":"in_progress","model":"gpt-5.6","output":[]}}`

	upstreamClosed := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack upstream: %v", err)
			return
		}
		defer conn.Close()
		defer close(upstreamClosed)
		fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: %s\r\n\r\n", websocketAccept(r.Header.Get("Sec-WebSocket-Key")))
		if err := rw.Flush(); err != nil {
			t.Errorf("flush upstream handshake: %v", err)
			return
		}
		if _, _, err := readWebSocketFrame(rw); err != nil {
			t.Errorf("read client frame: %v", err)
			return
		}
		if err := writeWebSocketFrame(rw, false, 1, []byte(created)); err != nil {
			t.Errorf("write server frame: %v", err)
			return
		}
		if err := rw.Flush(); err != nil {
			t.Errorf("flush server frame: %v", err)
			return
		}
		_, _ = rw.ReadByte()
	}))
	t.Cleanup(up.Close)

	sink := newFakeSink()
	p, err := New(up.URL, sink)
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(p)
	t.Cleanup(front.Close)

	conn, reader, err := openTestWebSocket(front.URL+"/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := writeWebSocketFrame(conn, true, 1, []byte(request)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readWebSocketFrame(reader); err != nil {
		t.Fatal(err)
	}

	p.Close()
	select {
	case <-upstreamClosed:
	case <-time.After(time.Second):
		t.Fatal("upstream WebSocket remained open after proxy shutdown")
	}
	if _, _, err := readWebSocketFrame(reader); err == nil {
		t.Fatal("client WebSocket remained open after proxy shutdown")
	}
	select {
	case ex := <-sink.ch:
		if !ex.ClientAborted || string(ex.ReqBody) != request || !strings.Contains(string(ex.RespBody), created) {
			t.Errorf("active exchange was not flushed on shutdown: %+v", ex)
		}
	case <-time.After(time.Second):
		t.Fatal("active WebSocket exchange was lost on shutdown")
	}
}

type fanoutSink []record.Sink

func (s fanoutSink) Record(ex record.Exchange) {
	for _, sink := range s {
		sink.Record(ex)
	}
}

func openTestWebSocket(rawURL string, headers http.Header) (net.Conn, *bufio.Reader, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, nil, err
	}
	conn, err := net.Dial("tcp", u.Host)
	if err != nil {
		return nil, nil, err
	}
	fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: %s\r\n", u.RequestURI(), u.Host, testWebSocketKey)
	for name, values := range headers {
		for _, value := range values {
			fmt.Fprintf(conn, "%s: %s\r\n", name, value)
		}
	}
	io.WriteString(conn, "\r\n")

	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		conn.Close()
		return nil, nil, fmt.Errorf("WebSocket handshake = %s: %s", resp.Status, body)
	}
	if !strings.EqualFold(resp.Header.Get("Upgrade"), "websocket") ||
		resp.Header.Get("Sec-WebSocket-Accept") != websocketAccept(testWebSocketKey) {
		conn.Close()
		return nil, nil, fmt.Errorf("invalid WebSocket handshake headers: %v", resp.Header)
	}
	return conn, reader, nil
}

func websocketAccept(key string) string {
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func writeWebSocketFrame(w io.Writer, masked bool, opcode byte, payload []byte) error {
	header := []byte{0x80 | opcode}
	switch {
	case len(payload) < 126:
		header = append(header, byte(len(payload)))
	case len(payload) <= 0xffff:
		header = append(header, 126, 0, 0)
		binary.BigEndian.PutUint16(header[len(header)-2:], uint16(len(payload)))
	default:
		header = append(header, 127, 0, 0, 0, 0, 0, 0, 0, 0)
		binary.BigEndian.PutUint64(header[len(header)-8:], uint64(len(payload)))
	}
	if masked {
		header[1] |= 0x80
		mask := [4]byte{1, 2, 3, 4}
		header = append(header, mask[:]...)
		payload = append([]byte(nil), payload...)
		for i := range payload {
			payload[i] ^= mask[i%len(mask)]
		}
	}
	if _, err := w.Write(header); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func readWebSocketFrame(r io.Reader) (byte, []byte, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(r, header); err != nil {
		return 0, nil, err
	}
	size := uint64(header[1] & 0x7f)
	switch size {
	case 126:
		var extended [2]byte
		if _, err := io.ReadFull(r, extended[:]); err != nil {
			return 0, nil, err
		}
		size = uint64(binary.BigEndian.Uint16(extended[:]))
	case 127:
		var extended [8]byte
		if _, err := io.ReadFull(r, extended[:]); err != nil {
			return 0, nil, err
		}
		size = binary.BigEndian.Uint64(extended[:])
	}
	if size > 16<<20 {
		return 0, nil, fmt.Errorf("test frame too large: %d", size)
	}
	var mask [4]byte
	if header[1]&0x80 != 0 {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	payload := make([]byte, int(size))
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	if header[1]&0x80 != 0 {
		for i := range payload {
			payload[i] ^= mask[i%len(mask)]
		}
	}
	return header[0] & 0xf, payload, nil
}
