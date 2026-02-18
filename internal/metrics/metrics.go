package metrics

import (
	"net/http"
	"strconv"
	"strings"
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

	// Cache metrics.
	cacheSizeBytes = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "stargo_cache_size_bytes",
			Help: "Estimated memory size of the keyframe cache.",
		},
	)

	cacheEntries = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "stargo_cache_entries",
			Help: "Number of keyframes in cache.",
		},
	)

	cacheHitsTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "stargo_cache_hits_total",
			Help: "Total cache hits.",
		},
	)

	cacheMissesTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "stargo_cache_misses_total",
			Help: "Total cache misses.",
		},
	)

	cacheRegenerationDurationSeconds = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "stargo_cache_regeneration_duration_seconds",
			Help:    "Duration of cache regeneration operations.",
			Buckets: []float64{0.1, 0.5, 1, 2.5, 5, 10, 30, 60},
		},
	)

	cacheRegenerationErrorsTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "stargo_cache_regeneration_errors_total",
			Help: "Total cache regeneration errors.",
		},
	)

	cacheGracePeriodActive = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "stargo_cache_grace_period_active",
			Help: "1 if TLE cutover grace period is active, 0 otherwise.",
		},
	)

	cacheEvictionsTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "stargo_cache_evictions_total",
			Help: "Total expired cache entries removed.",
		},
	)

	// Single-satellite propagation endpoint metrics.
	propagateRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "stargo_propagate_requests_total",
			Help: "Total on-demand single-satellite propagation requests.",
		},
		[]string{"status"},
	)

	propagateDurationSeconds = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "stargo_propagate_duration_seconds",
			Help:    "Duration of single-satellite propagation requests.",
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1},
		},
	)

	// Stream metrics.
	streamsActive = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "stargo_streams_active",
			Help: "Number of active SSE connections.",
		},
	)

	streamBytesTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "stargo_stream_bytes_total",
			Help: "Total bytes sent to all streams.",
		},
	)

	streamMessagesTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "stargo_stream_messages_total",
			Help: "Total keyframe messages sent.",
		},
	)

	streamErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "stargo_stream_errors_total",
			Help: "Total stream errors by type.",
		},
		[]string{"type"},
	)

	streamConnectionsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "stargo_stream_connections_total",
			Help: "Total stream connections by status.",
		},
		[]string{"status"},
	)

	// Pass prediction metrics.
	passRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "stargo_pass_requests_total",
			Help: "Total pass prediction requests.",
		},
		[]string{"status"},
	)

	passDurationSeconds = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "stargo_pass_duration_seconds",
			Help:    "Duration of pass prediction requests in seconds.",
			Buckets: []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		},
	)

	passSatellitesPerRequest = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "stargo_pass_satellites_per_request",
			Help:    "Number of satellites per pass prediction request.",
			Buckets: []float64{1, 5, 10, 25, 50, 100},
		},
	)

	passRejectedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "stargo_pass_rejected_total",
			Help: "Total pass requests rejected by reason.",
		},
		[]string{"reason"},
	)

	passTimeoutTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "stargo_pass_timeout_total",
			Help: "Total pass requests that timed out.",
		},
	)

	passJobsActive = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "stargo_pass_jobs_active",
			Help: "Number of active pass prediction jobs.",
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
	prometheus.MustRegister(cacheSizeBytes)
	prometheus.MustRegister(cacheEntries)
	prometheus.MustRegister(cacheHitsTotal)
	prometheus.MustRegister(cacheMissesTotal)
	prometheus.MustRegister(cacheRegenerationDurationSeconds)
	prometheus.MustRegister(cacheRegenerationErrorsTotal)
	prometheus.MustRegister(cacheGracePeriodActive)
	prometheus.MustRegister(cacheEvictionsTotal)
	prometheus.MustRegister(propagateRequestsTotal)
	prometheus.MustRegister(propagateDurationSeconds)
	prometheus.MustRegister(streamsActive)
	prometheus.MustRegister(streamBytesTotal)
	prometheus.MustRegister(streamMessagesTotal)
	prometheus.MustRegister(streamErrorsTotal)
	prometheus.MustRegister(streamConnectionsTotal)
	prometheus.MustRegister(passRequestsTotal)
	prometheus.MustRegister(passDurationSeconds)
	prometheus.MustRegister(passSatellitesPerRequest)
	prometheus.MustRegister(passRejectedTotal)
	prometheus.MustRegister(passTimeoutTotal)
	prometheus.MustRegister(passJobsActive)
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

// SetCacheSizeBytes sets the estimated cache memory size.
func SetCacheSizeBytes(n int64) {
	cacheSizeBytes.Set(float64(n))
}

// SetCacheEntries sets the number of keyframes in cache.
func SetCacheEntries(n int) {
	cacheEntries.Set(float64(n))
}

// IncCacheHits increments the cache hit counter.
func IncCacheHits() {
	cacheHitsTotal.Inc()
}

// IncCacheMisses increments the cache miss counter.
func IncCacheMisses() {
	cacheMissesTotal.Inc()
}

// ObserveCacheRegenerationDuration records a cache regeneration duration.
func ObserveCacheRegenerationDuration(d time.Duration) {
	cacheRegenerationDurationSeconds.Observe(d.Seconds())
}

// IncCacheRegenerationErrors increments the cache regeneration error counter.
func IncCacheRegenerationErrors() {
	cacheRegenerationErrorsTotal.Inc()
}

// SetCacheGracePeriodActive sets the grace period gauge.
func SetCacheGracePeriodActive(active bool) {
	if active {
		cacheGracePeriodActive.Set(1)
	} else {
		cacheGracePeriodActive.Set(0)
	}
}

// AddCacheEvictions adds to the cache eviction counter.
func AddCacheEvictions(n int) {
	cacheEvictionsTotal.Add(float64(n))
}

// IncStreamsActive increments the active streams gauge.
func IncStreamsActive() {
	streamsActive.Inc()
}

// DecStreamsActive decrements the active streams gauge.
func DecStreamsActive() {
	streamsActive.Dec()
}

// AddStreamBytes adds to the stream bytes counter.
func AddStreamBytes(n int64) {
	streamBytesTotal.Add(float64(n))
}

// IncStreamMessages increments the stream messages counter.
func IncStreamMessages() {
	streamMessagesTotal.Inc()
}

// IncStreamErrors increments the stream error counter for a given error type.
func IncStreamErrors(errType string) {
	streamErrorsTotal.WithLabelValues(errType).Inc()
}

// RecordPropagateRequest records a single-satellite propagation request.
func RecordPropagateRequest(status string, duration time.Duration) {
	propagateRequestsTotal.WithLabelValues(status).Inc()
	propagateDurationSeconds.Observe(duration.Seconds())
}

// IncStreamConnections increments the stream connections counter for a given status.
func IncStreamConnections(status string) {
	streamConnectionsTotal.WithLabelValues(status).Inc()
}

// RecordPassRequest records a pass prediction request.
func RecordPassRequest(status string, duration time.Duration, numSats int) {
	passRequestsTotal.WithLabelValues(status).Inc()
	passDurationSeconds.Observe(duration.Seconds())
	passSatellitesPerRequest.Observe(float64(numSats))
}

// IncPassRejected increments the pass rejected counter for a given reason.
func IncPassRejected(reason string) {
	passRejectedTotal.WithLabelValues(reason).Inc()
}

// IncPassTimeout increments the pass timeout counter.
func IncPassTimeout() {
	passTimeoutTotal.Inc()
}

// IncPassJobsActive increments the active pass jobs gauge.
func IncPassJobsActive() {
	passJobsActive.Inc()
}

// DecPassJobsActive decrements the active pass jobs gauge.
func DecPassJobsActive() {
	passJobsActive.Dec()
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

func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap returns the underlying ResponseWriter (required by http.ResponseController).
func (rw *responseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

// normalizeRoute maps a request path to a fixed route label to prevent
// Prometheus cardinality explosion from parameterized or unknown paths.
func normalizeRoute(path string) string {
	switch path {
	case "/healthz", "/readyz", "/metrics", "/",
		"/api/v1/test", "/api/v1/tle/metadata", "/api/v1/tle/fetch",
		"/api/v1/refresh-tles", "/api/v1/propagate/test",
		"/api/v1/cache/keyframes/latest", "/api/v1/cache/keyframes/at",
		"/api/v1/cache/stats", "/api/v1/stream/keyframes",
		"/api/v1/passes":
		return path
	}
	if strings.HasPrefix(path, "/api/v1/propagate/") {
		return "/api/v1/propagate/{norad_id}"
	}
	return "other"
}

// Middleware records request count and duration for each request.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(rw, r)

		duration := time.Since(start).Seconds()
		code := strconv.Itoa(rw.statusCode)
		route := normalizeRoute(r.URL.Path)

		httpRequestsTotal.WithLabelValues(route, r.Method, code).Inc()
		httpDurationSeconds.WithLabelValues(route, r.Method).Observe(duration)
	})
}
