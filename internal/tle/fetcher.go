package tle

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

const defaultSourceURL = "https://celestrak.org/NORAD/elements/gp.php?GROUP=starlink&FORMAT=tle"

// Fetcher retrieves raw TLE data from a remote source.
type Fetcher struct {
	sourceURL  string
	httpClient *http.Client
}

// NewFetcher creates a Fetcher for the given source URL.
func NewFetcher(sourceURL string) *Fetcher {
	if sourceURL == "" {
		sourceURL = defaultSourceURL
	}
	return &Fetcher{
		sourceURL: sourceURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// SourceURL returns the configured source URL.
func (f *Fetcher) SourceURL() string {
	return f.sourceURL
}

// Fetch performs an HTTP GET to retrieve raw TLE data.
func (f *Fetcher) Fetch(ctx context.Context) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.sourceURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching TLE data: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d from %s", resp.StatusCode, f.sourceURL)
	}

	// Cap body size to 50 MB to prevent OOM from oversized upstream responses.
	const maxTLEBodySize = 50 * 1024 * 1024
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTLEBodySize+1))
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}
	if len(body) > maxTLEBodySize {
		return nil, fmt.Errorf("response body exceeds %d byte limit", maxTLEBodySize)
	}

	return body, nil
}
