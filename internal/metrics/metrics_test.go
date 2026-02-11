package metrics

import "testing"

func TestNormalizeRoute(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		// Known exact routes.
		{"/healthz", "/healthz"},
		{"/readyz", "/readyz"},
		{"/metrics", "/metrics"},
		{"/", "/"},
		{"/api/v1/test", "/api/v1/test"},
		{"/api/v1/tle/metadata", "/api/v1/tle/metadata"},
		{"/api/v1/tle/fetch", "/api/v1/tle/fetch"},
		{"/api/v1/propagate/test", "/api/v1/propagate/test"},
		{"/api/v1/cache/keyframes/latest", "/api/v1/cache/keyframes/latest"},
		{"/api/v1/cache/keyframes/at", "/api/v1/cache/keyframes/at"},
		{"/api/v1/cache/stats", "/api/v1/cache/stats"},
		{"/api/v1/stream/keyframes", "/api/v1/stream/keyframes"},

		// Parameterized propagate routes collapse to one label.
		{"/api/v1/propagate/25544", "/api/v1/propagate/{norad_id}"},
		{"/api/v1/propagate/44713", "/api/v1/propagate/{norad_id}"},
		{"/api/v1/propagate/99999", "/api/v1/propagate/{norad_id}"},
		{"/api/v1/propagate/1", "/api/v1/propagate/{norad_id}"},

		// Unknown/bot paths collapse to "other".
		{"/wp-admin", "other"},
		{"/robots.txt", "other"},
		{"/.env", "other"},
		{"/api/v2/something", "other"},
		{"/favicon.ico", "other"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := normalizeRoute(tt.path)
			if got != tt.want {
				t.Errorf("normalizeRoute(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

// TestMetricsCardinality verifies that 100 unique NORAD IDs produce
// exactly 1 distinct path label, not 100.
func TestMetricsCardinality(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		label := normalizeRoute("/api/v1/propagate/" + string(rune('0'+i%10)) + string(rune('0'+i/10)))
		seen[label] = true
	}
	if len(seen) != 1 {
		t.Errorf("expected 1 unique label for parameterized paths, got %d: %v", len(seen), seen)
	}
}
