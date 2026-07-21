package cmd

import (
	"io"
	"testing"
)

// TestResourceSourceLazyOnce verifies the lazy resource source: it does not read
// the file until the first reader() call, decompresses at most once across many
// calls, and hands out independent readers over the full bytes. This is the
// memory fix — files whose DictIDs are never resolved are never materialized.
func TestResourceSourceLazyOnce(t *testing.T) {
	calls := 0
	// Non-CESF payload → decompressResourceBytes returns it as-is (no real
	// inflate needed to exercise the lazy/once behavior).
	payload := []byte("this is a plain (non-CESF) ESF stream stand-in")
	src := &resourceSource{open: func() ([]byte, error) {
		calls++
		return append([]byte(nil), payload...), nil
	}}

	if calls != 0 {
		t.Fatalf("open() called %d times before any reader() — should be lazy", calls)
	}

	r1, err := src.reader()
	if err != nil {
		t.Fatal(err)
	}
	r2, err := src.reader()
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("open() called %d times across two reader() calls — want exactly 1 (once + cache)", calls)
	}

	// Independent readers, each over the full decompressed bytes.
	b1, _ := io.ReadAll(r1)
	b2, _ := io.ReadAll(r2)
	if string(b1) != string(payload) {
		t.Fatalf("reader 1 bytes = %q, want %q", b1, payload)
	}
	if string(b2) != string(payload) {
		t.Fatalf("reader 2 bytes = %q, want %q (independent cursors)", b2, payload)
	}
}

// TestResourceSourceOpenError verifies a failed open is cached and surfaced,
// not retried on every lookup, and never panics.
func TestResourceSourceOpenError(t *testing.T) {
	calls := 0
	src := &resourceSource{open: func() ([]byte, error) {
		calls++
		return nil, io.ErrUnexpectedEOF
	}}
	if _, err := src.reader(); err == nil {
		t.Fatal("expected error from failing open")
	}
	if _, err := src.reader(); err == nil {
		t.Fatal("expected cached error on second call")
	}
	if calls != 1 {
		t.Fatalf("open() called %d times — want 1 (error cached via sync.Once)", calls)
	}
}
