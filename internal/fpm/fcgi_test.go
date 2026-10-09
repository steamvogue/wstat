package fpm

import (
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

// fakeFCGIServer answers one FastCGI responder request with a canned body,
// echoing back the received PARAMS for protocol assertions.
type fakeFCGI struct {
	ln       net.Listener
	received map[string]string
	body     string
}

func startFakeFCGI(t *testing.T, body string) *fakeFCGI {
	t.Helper()
	ln, err := net.Listen("unix", t.TempDir()+"/fpm.sock")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeFCGI{ln: ln, received: map[string]string{}, body: body}
	go f.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return f
}

func (f *fakeFCGI) serve() {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		f.handle(conn)
	}
}

func (f *fakeFCGI) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	hdr := make([]byte, 8)
	var params []byte
	for {
		if _, err := io.ReadFull(conn, hdr); err != nil {
			return
		}
		cl := int(hdr[4])<<8 | int(hdr[5])
		body := make([]byte, cl)
		if _, err := io.ReadFull(conn, body); err != nil {
			return
		}
		switch hdr[1] {
		case fcgiParams:
			params = append(params, body...)
			if cl == 0 {
				goto respond
			}
		case fcgiStdin:
			if cl == 0 {
				goto respond
			}
		case fcgiBeginRequest:
			// nothing to capture
		}
	}
respond:
	f.received = decodeParams(params)
	// The fake server intentionally ignores write errors: the client will
	// surface them.
	_ = writeRecord(conn, 1, fcgiStdout, []byte(f.body))
	_ = writeRecord(conn, 1, fcgiStdout, nil)
	end := make([]byte, 8)
	binary.BigEndian.PutUint32(end[0:4], 200) // app status
	_ = writeRecord(conn, 1, fcgiEndRequest, end)
}

func decodeParams(b []byte) map[string]string {
	out := map[string]string{}
	for len(b) > 0 {
		kl := readLen(&b)
		vl := readLen(&b)
		if int(kl) > len(b) || int(vl) > len(b) {
			return out
		}
		k := string(b[:kl])
		b = b[kl:]
		v := string(b[:vl])
		b = b[vl:]
		out[k] = v
	}
	return out
}

func readLen(b *[]byte) int {
	first := (*b)[0]
	*b = (*b)[1:]
	if first < 128 {
		return int(first)
	}
	var buf [4]byte
	buf[0] = first
	copy(buf[1:], (*b)[:3])
	*b = (*b)[3:]
	return int(binary.BigEndian.Uint32(buf[:]) &^ 0x80000000)
}

func TestFCGICallRoundtrip(t *testing.T) {
	f := startFakeFCGI(t, `{"pool":"www","active processes":2}`)
	stdout, stderr, status, err := fcgiCall("unix", f.ln.Addr().String(),
		map[string]string{
			"SCRIPT_NAME":    "/status",
			"REQUEST_METHOD": "GET",
			"QUERY_STRING":   "json&full",
		}, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if status != 200 {
		t.Errorf("app status = %d", status)
	}
	if string(stdout) != `{"pool":"www","active processes":2}` {
		t.Errorf("stdout = %q", stdout)
	}
	if len(stderr) != 0 {
		t.Errorf("stderr = %q", stderr)
	}
	for k, want := range map[string]string{
		"SCRIPT_NAME":  "/status",
		"QUERY_STRING": "json&full",
	} {
		if f.received[k] != want {
			t.Errorf("param %s = %q, want %q", k, f.received[k], want)
		}
	}
}

func TestFCGICallConnRefused(t *testing.T) {
	if _, _, _, err := fcgiCall("unix", t.TempDir()+"/missing.sock", nil, time.Second); err == nil {
		t.Fatal("expected error for missing socket")
	}
}

func TestParseListen(t *testing.T) {
	cases := []struct {
		in            string
		network, addr string
	}{
		{"unix:/run/php/x.sock", "unix", "/run/php/x.sock"},
		{"/run/php/x.sock", "unix", "/run/php/x.sock"},
		{"127.0.0.1:9000", "tcp", "127.0.0.1:9000"},
		{"[::1]:9000", "tcp", "[::1]:9000"},
	}
	for _, c := range cases {
		n, a, ok := parseListen(c.in)
		if !ok || n != c.network || a != c.addr {
			t.Errorf("parseListen(%q) = %s %s %v", c.in, n, a, ok)
		}
	}
	if _, _, ok := parseListen(""); ok {
		t.Error("empty listen must not parse")
	}
}
