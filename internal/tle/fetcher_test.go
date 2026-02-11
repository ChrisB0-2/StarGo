package tle

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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

	fetcher := NewFetcher(server.URL)
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

	fetcher := NewFetcher(server.URL)
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

	fetcher := NewFetcher(server.URL)
	_, err := fetcher.Fetch(context.Background())
	if err == nil {
		t.Fatal("expected error for 500 response, got nil")
	}
}
