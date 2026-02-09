package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	httpRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "stargo_http_requests_total",
			Help: "Total number of HTTP requests.",
		},
		[]string{"path", "method", "code"},
	)

	httpDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "stargo_http_duration_seconds",
			Help:    "HTTP request duration in seconds.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"path", "method"},
	)

	tleFetchTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "stargo_tle_fetch_total",
			Help: "Total number of TLE fetch operations.",
		},
		[]string{"status"},
	)

	tleFetchDurationSeconds = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "stargo_tle_fetch_duration_seconds",
			Help:    "Duration of TLE fetch operations in seconds.",
			Buckets: prometheus.DefBuckets,
		},
	)

	tleDatasetAgeSeconds = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "stargo_tle_dataset_age_seconds",
			Help: "Age of the current TLE dataset in seconds.",
		},
	)

	tleDatasetCount = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "stargo_tle_dataset_count",
			Help: "Number of satellites in the current TLE dataset.",
		},
	)

	tleParseErrorsTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "stargo_tle_parse_errors_total",
			Help: "Total number of TLE parse errors.",
		},
	)

	// Propagation metrics.
	propagationDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "stargo_propagation_duration_seconds",
			Help:    "Duration of propagation operations in seconds.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		},
		[]string{"operation"},
	)

	propagationSatellitesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "stargo_propagation_satellites_total",
			Help: "Total number of satellites propagated.",
		},
		[]string{"status"},
	)

	propagationErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "stargo_propagation_errors_total",
			Help: "Total number of propagation errors.",
		},
		[]string{"type"},
	)

	propagationWorkersActive = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "stargo_propagation_workers_active",
			Help: "Number of active propagation workers.",
		},
	)
)

func init() {
	prometheus.MustRegister(httpRequestsTotal)
	prometheus.MustRegister(httpDurationSeconds)
	prometheus.MustRegister(tleFetchTotal)
	prometheus.MustRegister(tleFetchDurationSeconds)
	prometheus.MustRegister(tleDatasetAgeSeconds)
	prometheus.MustRegister(tleDatasetCount)
	prometheus.MustRegister(tleParseErrorsTotal)
	prometheus.MustRegister(propagationDurationSeconds)
	prometheus.MustRegister(propagationSatellitesTotal)
	prometheus.MustRegister(propagationErrorsTotal)
	prometheus.MustRegister(propagationWorkersActive)
}

// Handler returns the Prometheus metrics HTTP handler.
func Handler() http.Handler {
	return promhttp.Handler()
}

// RecordTLEFetch records a TLE fetch operation with status and duration.
func RecordTLEFetch(status string, duration time.Duration) {
	tleFetchTotal.WithLabelValues(status).Inc()
	tleFetchDurationSeconds.Observe(duration.Seconds())
}

// SetTLEDatasetAge sets the current TLE dataset age gauge.
func SetTLEDatasetAge(seconds float64) {
	tleDatasetAgeSeconds.Set(seconds)
}

// SetTLEDatasetCount sets the current TLE dataset satellite count gauge.
func SetTLEDatasetCount(n int) {
	tleDatasetCount.Set(float64(n))
}

// IncTLEParseErrors increments the TLE parse error counter.
func IncTLEParseErrors() {
	tleParseErrorsTotal.Inc()
}

// RecordPropagation records a batch propagation result.
func RecordPropagation(duration time.Duration, successCount, errorCount int) {
	propagationDurationSeconds.WithLabelValues("propagate").Observe(duration.Seconds())
	propagationSatellitesTotal.WithLabelValues("success").Add(float64(successCount))
	propagationSatellitesTotal.WithLabelValues("error").Add(float64(errorCount))
}

// IncPropagationErrors increments the propagation error counter for a given error type.
func IncPropagationErrors(errType string) {
	propagationErrorsTotal.WithLabelValues(errType).Inc()
}

// SetPropagationWorkersActive sets the number of active propagation workers.
func SetPropagationWorkersActive(n int) {
	propagationWorkersActive.Set(float64(n))
}

// responseWriter wraps http.ResponseWriter to capture the status code.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// Middleware records request count and duration for each request.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(rw, r)

		duration := time.Since(start).Seconds()
		code := strconv.Itoa(rw.statusCode)

		httpRequestsTotal.WithLabelValues(r.URL.Path, r.Method, code).Inc()
		httpDurationSeconds.WithLabelValues(r.URL.Path, r.Method).Observe(duration)
	})
}
