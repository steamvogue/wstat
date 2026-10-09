package fpm

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"time"
)

const probeTimeout = 2 * time.Second

func dialTimeout(network, addr string, d time.Duration) (net.Conn, error) {
	return net.DialTimeout(network, addr, d)
}

// Status is the php-fpm /status?json payload.
type Status struct {
	Pool               string `json:"pool"`
	ProcessManager     string `json:"process manager"`
	StartTime          int64  `json:"start time"`
	StartSince         int64  `json:"start since"`
	AcceptedConn       int64  `json:"accepted conn"`
	ListenQueue        int64  `json:"listen queue"`
	MaxListenQueue     int64  `json:"max listen queue"`
	ListenQueueLen     int64  `json:"listen queue len"`
	IdleProcesses      int64  `json:"idle processes"`
	ActiveProcesses    int64  `json:"active processes"`
	TotalProcesses     int64  `json:"total processes"`
	MaxActiveProcesses int64  `json:"max active processes"`
	MaxChildrenReached int64  `json:"max children reached"`
	SlowRequests       int64  `json:"slow requests"`
}

// Process is one entry of the &full status payload.
type Process struct {
	PID               int     `json:"pid"`
	State             string  `json:"state"`
	StartSince        int64   `json:"start since"`
	Requests          int64   `json:"requests"`
	RequestDuration   int64   `json:"request duration"`
	RequestMethod     string  `json:"request method"`
	RequestURI        string  `json:"request uri"`
	LastRequestCPU    float64 `json:"last request cpu"`
	LastRequestMemory int64   `json:"last request memory"`
}

type statusFull struct {
	Status
	Processes []Process `json:"processes"`
}

// QueryStatus fetches the pool status over FastCGI (GET <statusPath>?json&full).
// statusPath defaults to /status when the pool doesn't declare one.
func QueryStatus(network, addr, statusPath string) (*Status, []Process, error) {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	return queryStatusContext(ctx, network, addr, statusPath, true)
}

func queryStatusContext(ctx context.Context, network, addr, statusPath string, full bool) (*Status, []Process, error) {
	if statusPath == "" {
		statusPath = "/status"
	}
	query := "json"
	if full {
		query += "&full"
	}
	params := map[string]string{
		"SCRIPT_NAME":     statusPath,
		"SCRIPT_FILENAME": statusPath,
		"REQUEST_METHOD":  "GET",
		"REQUEST_URI":     statusPath + "?" + query,
		"QUERY_STRING":    query,
		"CONTENT_TYPE":    "text/plain",
		"CONTENT_LENGTH":  "0",
		"HTTP_HOST":       "localhost",
	}
	stdout, stderr, _, err := fcgiCallContext(ctx, network, addr, params)
	if err != nil {
		return nil, nil, err
	}
	body := extractJSON(stdout)
	if body == nil {
		msg := strings.TrimSpace(string(stderr))
		if msg == "" {
			msg = strings.TrimSpace(string(stdout))
		}
		if msg == "" {
			msg = "empty response"
		}
		return nil, nil, &StatusError{Reason: msg}
	}
	var payload statusFull
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, nil, err
	}
	return &payload.Status, payload.Processes, nil
}

// extractJSON finds the JSON object inside a status response (which may be
// preceded by headers or followed by HTML in "Primary script unknown" cases).
func extractJSON(b []byte) []byte {
	s := string(b)
	start := strings.Index(s, "{")
	if start < 0 {
		return nil
	}
	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return []byte(s[start : i+1])
			}
		}
	}
	return nil
}

// StatusError carries the (non-JSON) response body, typically "Access denied"
// or "Primary script unknown" when pm.status_path is not configured.
type StatusError struct{ Reason string }

func (e *StatusError) Error() string { return "fpm status: " + e.Reason }

// IsStatusDisabled reports whether the error indicates pm.status_path is off.
func IsStatusDisabled(err error) bool {
	se, ok := err.(*StatusError)
	if !ok {
		return false
	}
	r := strings.ToLower(se.Reason)
	return strings.Contains(r, "primary script unknown") ||
		strings.Contains(r, "access denied") ||
		strings.Contains(r, "not found")
}
