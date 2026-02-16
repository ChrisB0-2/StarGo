package tle

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

const defaultSourceURL = "https://celestrak.org/NORAD/elements/gp.php?GROUP=starlink&FORMAT=tle"

// Fetcher retrieves raw TLE data from one or more remote sources.
type Fetcher struct {
	sourceURL  string
	extraURLs  []string
	logger     *slog.Logger
	httpClient *http.Client
}

// NewFetcher creates a Fetcher for the given source URL and optional extra URLs.
// Extra URLs are fetched alongside the primary source and their results are appended.
// Failures on extra URLs are logged but do not fail the overall fetch.
func NewFetcher(sourceURL string, logger *slog.Logger, extraURLs ...string) *Fetcher {
	if sourceURL == "" {
		sourceURL = defaultSourceURL
	}
	return &Fetcher{
		sourceURL: sourceURL,
		extraURLs: extraURLs,
		logger:    logger,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// SourceURL returns the configured primary source URL.
func (f *Fetcher) SourceURL() string {
	return f.sourceURL
}

// Fetch retrieves raw TLE data from the primary source and all extra sources.
// Results are concatenated. Extra source failures are logged but do not fail the fetch.
func (f *Fetcher) Fetch(ctx context.Context) ([]byte, error) {
	primary, err := f.fetchURL(ctx, f.sourceURL)
	if err != nil {
		return nil, err
	}

	for _, url := range f.extraURLs {
		extra, err := f.fetchURL(ctx, url)
		if err != nil {
			f.logger.Warn("extra TLE source fetch failed", "url", url, "error", err)
			continue
		}
		primary = append(primary, '\n')
		primary = append(primary, extra...)
	}

	return primary, nil
}

// fetchURL performs an HTTP GET to retrieve raw TLE data from a single URL.
func (f *Fetcher) fetchURL(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching TLE data from %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d from %s", resp.StatusCode, url)
	}

	// Cap body size to 50 MB to prevent OOM from oversized upstream responses.
	const maxTLEBodySize = 50 * 1024 * 1024
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTLEBodySize+1))
	if err != nil {
		return nil, fmt.Errorf("reading response body from %s: %w", url, err)
	}
	if len(body) > maxTLEBodySize {
		return nil, fmt.Errorf("response body exceeds %d byte limit from %s", maxTLEBodySize, url)
	}

	return body, nil
}
