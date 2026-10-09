// Package fpm integrates php-fpm: pool discovery, FastCGI status queries
// (pm.status_path), process stats from /proc, and access/slow log parsing.
package fpm

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"time"
)

// FastCGI protocol constants (subset used by the responder role).
const (
	fcgiBeginRequest = 1
	fcgiEndRequest   = 3
	fcgiParams       = 4
	fcgiStdout       = 6
	fcgiStderr       = 7
	fcgiStdin        = 8

	fcgiRoleResponder = 1
)

// fcgiCall performs a minimal FastCGI responder request and returns the
// collected stdout/stderr streams and the application status code.
func fcgiCall(network, addr string, params map[string]string, timeout time.Duration) (stdout, stderr []byte, appStatus int, err error) {
	conn, err := net.DialTimeout(network, addr, timeout)
	if err != nil {
		return nil, nil, 0, err
	}
	defer func() { _ = conn.Close() }()
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(false)
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))

	reqID := uint16(1)
	// BEGIN_REQUEST
	begin := []byte{0, fcgiRoleResponder, 0, 0, 0, 0, 0, 0}
	if err := writeRecord(conn, reqID, fcgiBeginRequest, begin); err != nil {
		return nil, nil, 0, err
	}
	// PARAMS (chunked) then an empty record to terminate the stream.
	enc := encodeParams(params)
	for len(enc) > 0 {
		n := len(enc)
		if n > 0xFFFF {
			n = 0xFFFF
		}
		if err := writeRecord(conn, reqID, fcgiParams, enc[:n]); err != nil {
			return nil, nil, 0, err
		}
		enc = enc[n:]
	}
	if err := writeRecord(conn, reqID, fcgiParams, nil); err != nil {
		return nil, nil, 0, err
	}
	// STDIN: empty (GET request).
	if err := writeRecord(conn, reqID, fcgiStdin, nil); err != nil {
		return nil, nil, 0, err
	}

	// Read until END_REQUEST.
	var out, errOut bytes.Buffer
	hdr := make([]byte, 8)
	for {
		if _, err := io.ReadFull(conn, hdr); err != nil {
			return out.Bytes(), errOut.Bytes(), 0, err
		}
		if hdr[0] != 1 {
			return out.Bytes(), errOut.Bytes(), 0, errBadVersion
		}
		contentLen := int(hdr[4])<<8 | int(hdr[5])
		paddingLen := int(hdr[6])
		body := make([]byte, contentLen)
		if _, err := io.ReadFull(conn, body); err != nil {
			return out.Bytes(), errOut.Bytes(), 0, err
		}
		if paddingLen > 0 {
			if _, err := io.CopyN(io.Discard, conn, int64(paddingLen)); err != nil {
				return out.Bytes(), errOut.Bytes(), 0, err
			}
		}
		switch hdr[1] {
		case fcgiStdout:
			out.Write(body)
		case fcgiStderr:
			errOut.Write(body)
		case fcgiEndRequest:
			if contentLen >= 8 {
				appStatus = int(binary.BigEndian.Uint32(body[0:4]))
			}
			return out.Bytes(), errOut.Bytes(), appStatus, nil
		}
	}
}

var errBadVersion = errFCGIVersion{}

type errFCGIVersion struct{}

func (errFCGIVersion) Error() string { return "fcgi: unexpected protocol version" }

func writeRecord(w io.Writer, reqID uint16, recType byte, content []byte) error {
	hdr := [8]byte{
		1, recType,
		byte(reqID >> 8), byte(reqID),
		byte(len(content) >> 8), byte(len(content)),
		0, 0,
	}
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(content) > 0 {
		if _, err := w.Write(content); err != nil {
			return err
		}
	}
	return nil
}

// encodeParams serializes FastCGI name/value pairs.
func encodeParams(params map[string]string) []byte {
	var buf bytes.Buffer
	writeLen := func(n int) {
		if n < 128 {
			buf.WriteByte(byte(n))
			return
		}
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], uint32(n)|0x80000000)
		buf.Write(b[:])
	}
	for k, v := range params {
		writeLen(len(k))
		writeLen(len(v))
		buf.WriteString(k)
		buf.WriteString(v)
	}
	return buf.Bytes()
}

// parseListen converts a pool "listen" value into a dial target.
func parseListen(listen string) (network, addr string, ok bool) {
	switch {
	case listen == "":
		return "", "", false
	case bytes.HasPrefix([]byte(listen), []byte("unix:")):
		return "unix", listen[5:], true
	case listen[0] == '/':
		return "unix", listen, true
	default:
		return "tcp", listen, true
	}
}
