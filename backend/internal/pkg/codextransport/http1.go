package codextransport

import (
	"bytes"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// http1WireConn only changes header-name spelling. Request bodies, chunk
// boundaries, trailers and upgraded WebSocket frames remain byte-for-byte intact.
// req's HTTP/1 writer owns header ordering, including Host and Content-Length.
type http1WireConn struct {
	net.Conn
	spelling      map[string]string
	upgrade       bool
	header        bytes.Buffer
	bodyRemaining int64
	chunked       bool
	chunkPhase    byte
	chunkLine     []byte
}

func (c *http1WireConn) Write(input []byte) (int, error) {
	if c.upgrade {
		return c.Conn.Write(input)
	}
	p := input
	for len(p) > 0 {
		if !c.chunked && c.bodyRemaining > 0 {
			n := int64(len(p))
			if n > c.bodyRemaining {
				n = c.bodyRemaining
			}
			if _, err := c.Conn.Write(p[:n]); err != nil {
				_ = c.Close()
				return 0, err
			}
			c.bodyRemaining -= n
			p = p[n:]
			continue
		}
		if c.chunked {
			n, err := c.consumeChunk(p)
			if err != nil {
				_ = c.Close()
				return 0, err
			}
			if _, err = c.Conn.Write(p[:n]); err != nil {
				_ = c.Close()
				return 0, err
			}
			p = p[n:]
			continue
		}
		// Headers may be split across writes. Bound only the header allocation,
		// retaining net/http's existing default header-size boundary.
		_ = c.header.WriteByte(p[0]) // bytes.Buffer.WriteByte cannot fail.
		p = p[1:]
		if c.header.Len() > 1<<20 {
			_ = c.Close()
			return 0, fmt.Errorf("codex HTTP1 headers exceed 1 MiB")
		}
		if !bytes.HasSuffix(c.header.Bytes(), []byte("\r\n\r\n")) {
			continue
		}
		raw := c.header.Bytes()
		lines := bytes.Split(raw, []byte("\r\n"))
		for i := 1; i < len(lines); i++ {
			colon := bytes.IndexByte(lines[i], ':')
			if colon < 0 {
				continue
			}
			name := strings.ToLower(string(lines[i][:colon]))
			value := strings.TrimSpace(string(lines[i][colon+1:]))
			if spelling := c.spelling[name]; spelling != "" {
				copy(lines[i][:colon], spelling)
			}
			switch name {
			case "content-length":
				length, err := strconv.ParseInt(value, 10, 64)
				if err != nil || length < 0 {
					_ = c.Close()
					return 0, fmt.Errorf("invalid HTTP1 Content-Length")
				}
				c.bodyRemaining = length
			case "transfer-encoding":
				c.chunked = strings.Contains(strings.ToLower(value), "chunked")
				c.chunkPhase = 's'
			case "upgrade":
				c.upgrade = strings.EqualFold(value, "websocket")
			}
		}
		if _, err := c.Conn.Write(raw); err != nil {
			_ = c.Close()
			return 0, err
		}
		c.header.Reset()
		if c.upgrade && len(p) > 0 {
			if _, err := c.Conn.Write(p); err != nil {
				return 0, err
			}
			p = nil
		}
	}
	return len(input), nil
}

func (c *http1WireConn) consumeChunk(p []byte) (int, error) {
	n := 0
	for n < len(p) && c.chunked {
		if c.chunkPhase == 'd' {
			take := int64(len(p) - n)
			if take > c.bodyRemaining {
				take = c.bodyRemaining
			}
			n += int(take)
			c.bodyRemaining -= take
			if c.bodyRemaining == 0 {
				c.chunkPhase = 'r'
			}
			continue
		}
		c.chunkLine = append(c.chunkLine, p[n])
		n++
		if len(c.chunkLine) > 1<<20 {
			return 0, fmt.Errorf("HTTP1 chunk control line too long")
		}
		if !bytes.HasSuffix(c.chunkLine, []byte("\r\n")) {
			continue
		}
		line := strings.TrimSuffix(string(c.chunkLine), "\r\n")
		c.chunkLine = c.chunkLine[:0]
		switch c.chunkPhase {
		case 's':
			if i := strings.IndexByte(line, ';'); i >= 0 {
				line = line[:i]
			}
			size, err := strconv.ParseInt(line, 16, 64)
			if err != nil || size < 0 {
				return 0, fmt.Errorf("invalid HTTP1 chunk size")
			}
			if size == 0 {
				c.chunkPhase = 't'
			} else {
				c.bodyRemaining = size
				c.chunkPhase = 'd'
			}
		case 'r':
			if line != "" {
				return 0, fmt.Errorf("invalid chunk delimiter")
			}
			c.chunkPhase = 's'
		case 't':
			if line == "" {
				c.chunked = false
				c.chunkPhase = 0
			}
		}
	}
	return n, nil
}
