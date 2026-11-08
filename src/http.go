package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// retryableError marks failures worth retrying (network, timeout, 429, 5xx); after is the server-requested delay.
type retryableError struct {
	error
	after time.Duration
}

func (e retryableError) Unwrap() error { return e.error }

// apiClient posts JSON to an LLM endpoint with a per-request timeout.
type apiClient struct {
	timeout time.Duration
	headers map[string]string
}

// post sends body and hands the response body to read. When streaming, the timeout is an
// idle limit reset on every read; otherwise it bounds the whole request.
func (c apiClient) post(ctx context.Context, url string, body any, stream bool, read func(io.Reader) error) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var timedOut atomic.Bool
	timer := time.AfterFunc(c.timeout, func() { timedOut.Store(true); cancel() })
	defer timer.Stop()

	err = c.do(ctx, url, payload, func(r io.Reader) error {
		if stream {
			r = idleReader{r, timer, c.timeout}
		}
		return read(r)
	})
	if err != nil && timedOut.Load() {
		return retryableError{error: fmt.Errorf("request timed out after %s", c.timeout)}
	}
	return err
}

func (c apiClient) do(ctx context.Context, url string, payload []byte, read func(io.Reader) error) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return retryableError{error: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		err := fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			secs, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
			return retryableError{error: err, after: time.Duration(secs) * time.Second}
		}
		return err
	}
	return read(resp.Body)
}

type idleReader struct {
	r     io.Reader
	timer *time.Timer
	d     time.Duration
}

func (r idleReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.timer.Reset(r.d)
	return n, err
}

// readSSE calls onData with the payload of each "data:" line until it returns io.EOF (the
// terminal event). A stream that ends before that is truncated and reported as retryable.
func readSSE(r io.Reader, onData func([]byte) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(nil, 16<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data:")
		if !ok {
			continue
		}
		if err := onData([]byte(strings.TrimSpace(data))); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
	if err := sc.Err(); err != nil {
		return retryableError{error: err}
	}
	return retryableError{error: errors.New("stream ended before the final event")}
}
