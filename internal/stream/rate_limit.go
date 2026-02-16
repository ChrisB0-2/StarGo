package stream

import (
	"sync"
)

// streamLimiter tracks concurrent SSE connections per IP and globally.
type streamLimiter struct {
	mu          sync.Mutex
	connections map[string]int
	total       int
	maxPerIP    int
	maxTotal    int
}

func newStreamLimiter(maxPerIP int) *streamLimiter {
	return &streamLimiter{
		connections: make(map[string]int),
		maxPerIP:    maxPerIP,
		maxTotal:    1000, // Default global cap.
	}
}

// acquire attempts to register a new connection for the given IP.
// Returns false if the IP or global limit has been reached.
func (l *streamLimiter) acquire(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.total >= l.maxTotal {
		return false
	}
	if l.connections[ip] >= l.maxPerIP {
		return false
	}

	l.connections[ip]++
	l.total++
	return true
}

// release decrements the connection count for the given IP.
func (l *streamLimiter) release(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.connections[ip]--
	l.total--
	if l.connections[ip] <= 0 {
		delete(l.connections, ip)
	}
}

// count returns the number of active connections for the given IP.
func (l *streamLimiter) count(ip string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.connections[ip]
}

