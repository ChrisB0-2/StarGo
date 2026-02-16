package tle

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var testLogger = slog.New(slog.NewJSONHandler(io.Discard, nil))

// TestFetcherBodyLimit verifies that responses exceeding the 50 MB limit
// return an error instead of consuming unbounded memory.
func TestFetcherBodyLimit(t *testing.T) {
	// Server streams zeroes indefinitely until the client stops reading.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		// Write in 1 MB chunks to exceed the 50 MB limit.
		chunk := strings.Repeat("A", 1024*1024)
		for i := 0; i < 52; i++ {
			if _, err := w.Write([]byte(chunk)); err != nil {
				return // Client closed connection.
			}
		}
	}))
	defer server.Close()

	fetcher := NewFetcher(server.URL, testLogger)
	_, err := fetcher.Fetch(context.Background())
	if err == nil {
		t.Fatal("expected error for oversized response, got nil")
	}
	if !strings.Contains(err.Error(), "byte limit") {
		t.Errorf("expected body limit error, got: %v", err)
	}
}

// TestFetcherSuccess verifies normal fetch operation.
func TestFetcherSuccess(t *testing.T) {
	body := "ISS (ZARYA)\n1 25544U 98067A   24100.50000000  .00016717  00000-0  10270-3 0  9005\n2 25544  51.6400 100.0000 0001000   0.0000   0.0000 15.50000000    09\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(body))
	}))
	defer server.Close()

	fetcher := NewFetcher(server.URL, testLogger)
	data, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != body {
		t.Errorf("body mismatch: got %d bytes, want %d", len(data), len(body))
	}
}

// TestFetcherHTTPError verifies error handling for non-200 responses.
func TestFetcherHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	fetcher := NewFetcher(server.URL, testLogger)
	_, err := fetcher.Fetch(context.Background())
	if err == nil {
		t.Fatal("expected error for 500 response, got nil")
	}
}

// TestFetcherExtraURLs verifies that extra URLs are fetched and concatenated.
func TestFetcherExtraURLs(t *testing.T) {
	starlink := "STARLINK-1007\n1 44713U 19074A   24100.50000000  .00001000  00000-0  10000-4 0  9995\n2 44713  53.0000 200.0000 0001500  90.0000 270.0000 15.06000000    05\n"
	iss := "ISS (ZARYA)\n1 25544U 98067A   24100.50000000  .00016717  00000-0  10270-3 0  9005\n2 25544  51.6400 100.0000 0001000   0.0000   0.0000 15.50000000    09\n"

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(starlink))
	}))
	defer primary.Close()

	extra := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(iss))
	}))
	defer extra.Close()

	fetcher := NewFetcher(primary.URL, testLogger, extra.URL)
	data, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Parse and verify both satellites are present.
	entries, err := Parse(strings.NewReader(string(data)), testLogger)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	ids := map[int]bool{}
	for _, e := range entries {
		ids[e.NORADID] = true
	}
	if !ids[44713] {
		t.Error("missing STARLINK-1007 (44713)")
	}
	if !ids[25544] {
		t.Error("missing ISS (25544)")
	}
}

// TestFetcherExtraURLFailure verifies that a failing extra URL doesn't break the primary fetch.
func TestFetcherExtraURLFailure(t *testing.T) {
	starlink := "STARLINK-1007\n1 44713U 19074A   24100.50000000  .00001000  00000-0  10000-4 0  9995\n2 44713  53.0000 200.0000 0001500  90.0000 270.0000 15.06000000    05\n"

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(starlink))
	}))
	defer primary.Close()

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failing.Close()

	fetcher := NewFetcher(primary.URL, testLogger, failing.URL)
	data, err := fetcher.Fetch(context.Background())
	if err != nil {
		t.Fatalf("primary fetch should succeed even when extra fails: %v", err)
	}

	entries, err := Parse(strings.NewReader(string(data)), testLogger)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry (primary only), got %d", len(entries))
	}
	if entries[0].NORADID != 44713 {
		t.Errorf("expected NORAD 44713, got %d", entries[0].NORADID)
	}
}
