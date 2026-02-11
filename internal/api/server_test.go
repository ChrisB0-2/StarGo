package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/star/stargo/internal/tle"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// TestPropagateCPUBudget verifies that requests exceeding the max positions
// budget are rejected with 400 instead of consuming unbounded CPU.
func TestPropagateCPUBudget(t *testing.T) {
	logger := testLogger()
	store := tle.NewStore()
	store.Set(&tle.TLEDataset{
		Source:    "test",
		FetchedAt: time.Now(),
		Satellites: []tle.TLEEntry{
			{NORADID: 25544, Name: "ISS",
				Line1: "1 25544U 98067A   24100.50000000  .00016717  00000-0  10270-3 0  9005",
				Line2: "2 25544  51.6400 100.0000 0001000   0.0000   0.0000 15.50000000    09",
			},
		},
	})

	handler := propagateSingleHandler(logger, store)

	// Register on a mux so PathValue works.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/propagate/{norad_id}", handler)

	tests := []struct {
		name       string
		query      string
		wantStatus int
	}{
		{
			name:       "max budget exceeded: horizon=86400 step=1",
			query:      "?horizon=86400&step=1",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "max budget exceeded: horizon=60000 step=5",
			query:      "?horizon=60000&step=5",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "within budget: default params",
			query:      "",
			wantStatus: http.StatusOK,
		},
		{
			name:       "within budget: horizon=3600 step=1",
			query:      "?horizon=3600&step=1",
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/propagate/25544"+tt.query, nil)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}

			if tt.wantStatus == http.StatusBadRequest {
				var resp map[string]any
				json.NewDecoder(w.Body).Decode(&resp)
				if resp["error"] == nil {
					t.Error("expected error field in response")
				}
				if resp["max_positions"] == nil {
					t.Error("expected max_positions field in response")
				}
			}
		})
	}
}
