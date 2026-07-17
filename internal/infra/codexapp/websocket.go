// Package codexapp is the self-contained Codex app-server client
// infrastructure: a hand-rolled WebSocket transport, the JSON-RPC framing,
// app-server subprocess management, the Plan and team TUI controllers, their
// status-file handshakes, and the hidden launch command builders.
package codexapp

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const webSocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

type websocketJSONConn struct {
	conn net.Conn
	br   *bufio.Reader
	mu   sync.Mutex
}

func dialWebSocket(rawURL string, timeout time.Duration) (*websocketJSONConn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse websocket URL: %w", err)
	}
	if u.Scheme != "ws" {
		return nil, fmt.Errorf("unsupported websocket URL scheme %q", u.Scheme)
	}
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.Dial("tcp", u.Host)
	if err != nil {
		return nil, err
	}
	if err = conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		_ = conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	keyBytes := make([]byte, 16)
	if _, err = rand.Read(keyBytes); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("generate websocket key: %w", err)
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	req := "GET " + path + " HTTP/1.1\r\n" +
		"Host: " + u.Host + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"
	if _, err = io.WriteString(conn, req); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("write websocket handshake: %w", err)
	}
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("read websocket handshake: %w", err)
	}
	// The handshake response body is unused: a 101 carries no body, and for any
	// other status the connection is discarded below. Close never reads from br.
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		_ = conn.Close()
		return nil, fmt.Errorf("websocket handshake returned %s", resp.Status)
	}
	if got, want := resp.Header.Get("Sec-WebSocket-Accept"), webSocketAccept(key); got != want {
		_ = conn.Close()
		return nil, fmt.Errorf("websocket handshake accept mismatch")
	}
	if err = conn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &websocketJSONConn{conn: conn, br: br}, nil
}

func webSocketAccept(key string) string {
	sum := sha1.Sum([]byte(key + webSocketGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func (w *websocketJSONConn) Send(v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return w.writeFrame(0x1, body)
}

func (w *websocketJSONConn) Receive() (appServerMessage, error) {
	payload, err := w.readTextMessage()
	if err != nil {
		return appServerMessage{}, err
	}
	return parseAppServerLine(payload)
}

func (w *websocketJSONConn) Close() error {
	if w == nil || w.conn == nil {
		return nil
	}
	_ = w.writeFrame(0x8, nil)
	return w.conn.Close()
}

func (w *websocketJSONConn) readTextMessage() ([]byte, error) {
	var fragments bytes.Buffer
	for {
		fin, opcode, payload, err := w.readFrame()
		if err != nil {
			return nil, err
		}
		switch opcode {
		case 0x1:
			if fin {
				return payload, nil
			}
			fragments.Write(payload)
		case 0x0:
			if fragments.Len() == 0 {
				return nil, fmt.Errorf("websocket continuation without initial text frame")
			}
			fragments.Write(payload)
			if fin {
				return fragments.Bytes(), nil
			}
		case 0x8:
			return nil, io.EOF
		case 0x9:
			if err := w.writeFrame(0xA, payload); err != nil {
				return nil, err
			}
		case 0xA:
			continue
		default:
			return nil, fmt.Errorf("unsupported websocket opcode 0x%x", opcode)
		}
	}
}

func (w *websocketJSONConn) readFrame() (bool, byte, []byte, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(w.br, header); err != nil {
		return false, 0, nil, err
	}
	fin := header[0]&0x80 != 0
	opcode := header[0] & 0x0f
	masked := header[1]&0x80 != 0
	length := uint64(header[1] & 0x7f)
	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(w.br, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(w.br, ext[:]); err != nil {
			return false, 0, nil, err
		}
		length = binary.BigEndian.Uint64(ext[:])
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(w.br, mask[:]); err != nil {
			return false, 0, nil, err
		}
	}
	if length > 64*1024*1024 {
		return false, 0, nil, fmt.Errorf("websocket frame too large: %d bytes", length)
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(w.br, payload); err != nil {
		return false, 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return fin, opcode, payload, nil
}

func (w *websocketJSONConn) writeFrame(opcode byte, payload []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	header := []byte{0x80 | opcode}
	length := len(payload)
	switch {
	case length <= 125:
		header = append(header, byte(0x80|length))
	case length <= 0xffff:
		header = append(header, 0x80|126)
		var ext [2]byte
		binary.BigEndian.PutUint16(ext[:], uint16(length))
		header = append(header, ext[:]...)
	default:
		header = append(header, 0x80|127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(length))
		header = append(header, ext[:]...)
	}
	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return fmt.Errorf("generate websocket mask: %w", err)
	}
	header = append(header, mask[:]...)
	masked := make([]byte, len(payload))
	for i, b := range payload {
		masked[i] = b ^ mask[i%4]
	}
	if _, err := w.conn.Write(header); err != nil {
		return err
	}
	if len(masked) == 0 {
		return nil
	}
	_, err := w.conn.Write(masked)
	return err
}
