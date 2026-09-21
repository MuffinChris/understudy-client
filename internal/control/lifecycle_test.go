package control

import (
	"context"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"
)

func TestServeReportsBusyPortWithoutCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := New(newStubBot(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, listener.Addr().String()) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "control api") {
			t.Fatalf("got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("busy port waited for context cancellation")
	}
}

func TestServeListenerFailureReleasesShutdownWorker(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := New(newStubBot(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	done := make(chan error, 1)
	go func() { done <- s.ServeListener(ctx, listener) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("closed listener succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("closed listener hung")
	}
}
