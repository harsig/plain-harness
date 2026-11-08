package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeAPI serves body with status for every request and records the last request body.
// A body that starts with "data:" is sent as a server-sent event stream.
func fakeAPI(t *testing.T, status int, body string) (url string, got func() map[string]any) {
	t.Helper()
	var req map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req = nil
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("request body: %v", err)
		}
		if strings.HasPrefix(body, "data:") {
			w.Header().Set("Content-Type", "text/event-stream")
		}
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, func() map[string]any { return req }
}

// sse joins events into a server-sent event stream.
func sse(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		b.WriteString("data: " + e + "\n\n")
	}
	return b.String()
}

func TestPostErrors(t *testing.T) {
	tests := []struct {
		status    int
		retryable bool
		after     time.Duration
	}{
		{status: http.StatusTooManyRequests, retryable: true, after: 7 * time.Second},
		{status: http.StatusServiceUnavailable, retryable: true, after: 7 * time.Second},
		{status: http.StatusBadRequest, retryable: false},
	}
	for _, tt := range tests {
		url, _ := fakeAPI(t, tt.status, `{"error":"x"}`)
		api := apiClient{timeout: 5 * time.Second}
		err := api.post(context.Background(), url, map[string]any{}, false, func(io.Reader) error { return nil })
		var r retryableError
		if errors.As(err, &r) != tt.retryable {
			t.Errorf("HTTP %d: retryable = %v, want %v (%v)", tt.status, !tt.retryable, tt.retryable, err)
		}
		if r.after != tt.after {
			t.Errorf("HTTP %d: after = %s, want %s", tt.status, r.after, tt.after)
		}
	}
}

func TestReadSSETruncated(t *testing.T) {
	err := readSSE(strings.NewReader(sse(`{}`)), func([]byte) error { return nil })
	var r retryableError
	if !errors.As(err, &r) {
		t.Fatalf("err = %v, want a retryable error for a stream without its final event", err)
	}
}
