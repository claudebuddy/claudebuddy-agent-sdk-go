package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"
)

func mcpTestCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	return ctx
}
func awaitMCP[T any](t *testing.T, ctx context.Context, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-ctx.Done():
		t.Fatal("MCP progress timeout")
		var zero T
		return zero
	}
}

func TestConnectionSerializesRepliesAndCancelsCapacityWait(t *testing.T) {
	ctx := mcpTestCtx(t)
	requestsR, requestsW := io.Pipe()
	responsesR, responsesW := io.Pipe()
	conn := &Connection{stdin: requestsW, stdout: responsesR, reader: bufio.NewReader(responsesR)}
	defer requestsR.Close()
	defer requestsW.Close()
	defer responsesR.Close()
	defer responsesW.Close()
	entered := make(chan JSONRPCRequest, 2)
	release := make(chan struct{})
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		scanner := bufio.NewScanner(requestsR)
		for scanner.Scan() {
			var req JSONRPCRequest
			if json.Unmarshal(scanner.Bytes(), &req) != nil {
				return
			}
			entered <- req
			select {
			case <-release:
			case <-ctx.Done():
				return
			}
			if json.NewEncoder(responsesW).Encode(JSONRPCResponse{ID: req.ID, Result: json.RawMessage(`{"ok":true}`)}) != nil {
				return
			}
		}
	}()
	first := make(chan error, 1)
	go func() { _, err := conn.sendRequest(ctx, "first", nil); first <- err }()
	awaitMCP(t, ctx, entered)
	waitCtx, cancel := context.WithCancel(ctx)
	second := make(chan error, 1)
	go func() { _, err := conn.sendRequest(waitCtx, "queued", nil); second <- err }()
	cancel()
	if err := awaitMCP(t, ctx, second); !errors.Is(err, context.Canceled) {
		t.Fatalf("queued cancellation %v", err)
	}
	close(release)
	if err := awaitMCP(t, ctx, first); err != nil {
		t.Fatal(err)
	}
	select {
	case req := <-entered:
		t.Fatalf("cancelled request reached server: %s", req.Method)
	default:
	}
	requestsW.Close()
	awaitMCP(t, ctx, serverDone)
}

type trackedReadCloser struct {
	reader io.ReadCloser
	mu     sync.Mutex
	active int
}

func (r *trackedReadCloser) Read(b []byte) (int, error) {
	r.mu.Lock()
	r.active++
	r.mu.Unlock()
	defer func() { r.mu.Lock(); r.active--; r.mu.Unlock() }()
	return r.reader.Read(b)
}
func (r *trackedReadCloser) Close() error { return r.reader.Close() }
func TestConnectionCancellationPoisonsAndJoinsStdioReader(t *testing.T) {
	ctx := mcpTestCtx(t)
	requestsR, requestsW := io.Pipe()
	responsesR, responsesW := io.Pipe()
	tracked := &trackedReadCloser{reader: responsesR}
	defer requestsR.Close()
	defer requestsW.Close()
	defer responsesR.Close()
	defer responsesW.Close()
	conn := &Connection{stdin: requestsW, stdout: tracked, reader: bufio.NewReader(tracked)}
	seen := make(chan struct{})
	serverDone := make(chan struct{})
	go func() { defer close(serverDone); bufio.NewReader(requestsR).ReadBytes('\n'); close(seen) }()
	callCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { _, err := conn.sendRequest(callCtx, "blocked", nil); done <- err }()
	awaitMCP(t, ctx, seen)
	for {
		tracked.mu.Lock()
		active := tracked.active
		tracked.mu.Unlock()
		if active > 0 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	if err := awaitMCP(t, ctx, done); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	tracked.mu.Lock()
	active := tracked.active
	tracked.mu.Unlock()
	if active != 0 {
		t.Fatalf("reader goroutine not joined: %d", active)
	}
	if _, err := conn.sendRequest(ctx, "must-not-reuse", nil); err == nil {
		t.Fatal("poisoned connection reused")
	}
	awaitMCP(t, ctx, serverDone)
}

func TestConnectionConcurrentRequestsDoNotCrossWire(t *testing.T) {
	ctx := mcpTestCtx(t)
	requestsR, requestsW := io.Pipe()
	responsesR, responsesW := io.Pipe()
	conn := &Connection{stdin: requestsW, stdout: responsesR, reader: bufio.NewReader(responsesR)}
	defer requestsR.Close()
	defer requestsW.Close()
	defer responsesR.Close()
	defer responsesW.Close()
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		scanner := bufio.NewScanner(requestsR)
		for scanner.Scan() {
			var req JSONRPCRequest
			if json.Unmarshal(scanner.Bytes(), &req) != nil {
				return
			}
			result, _ := json.Marshal(req.Method)
			if json.NewEncoder(responsesW).Encode(JSONRPCResponse{ID: req.ID, Result: result}) != nil {
				return
			}
		}
	}()
	done := make(chan error, 20)
	for i := 0; i < 20; i++ {
		go func(i int) {
			name := fmt.Sprint(i)
			raw, err := conn.sendRequest(ctx, name, nil)
			if err == nil && string(raw) != fmt.Sprintf("%q", name) {
				err = fmt.Errorf("request %s got %s", name, raw)
			}
			done <- err
		}(i)
	}
	for i := 0; i < 20; i++ {
		if err := awaitMCP(t, ctx, done); err != nil {
			t.Fatal(err)
		}
	}
	requestsW.Close()
	awaitMCP(t, ctx, serverDone)
}
