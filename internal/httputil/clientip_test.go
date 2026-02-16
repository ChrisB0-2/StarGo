package httputil

import (
	"net/http"
	"testing"
)

func TestClientIPRemoteAddr(t *testing.T) {
	tests := []struct {
		remoteAddr string
		want       string
	}{
		{"192.168.1.1:12345", "192.168.1.1"},
		{"[::1]:12345", "::1"},
		{"192.168.1.1", "192.168.1.1"},
	}
	for _, tt := range tests {
		r := &http.Request{RemoteAddr: tt.remoteAddr}
		got := ClientIP(r, false)
		if got != tt.want {
			t.Errorf("ClientIP(%q, false) = %q, want %q", tt.remoteAddr, got, tt.want)
		}
	}
}

func TestClientIPTrustProxy(t *testing.T) {
	tests := []struct {
		name       string
		xff        string
		xri        string
		remoteAddr string
		want       string
	}{
		{
			name:       "XFF single IP",
			xff:        "1.2.3.4",
			remoteAddr: "10.0.0.1:1234",
			want:       "1.2.3.4",
		},
		{
			name:       "XFF multiple IPs takes first",
			xff:        "1.2.3.4, 10.0.0.1, 10.0.0.2",
			remoteAddr: "10.0.0.3:1234",
			want:       "1.2.3.4",
		},
		{
			name:       "X-Real-IP fallback",
			xri:        "5.6.7.8",
			remoteAddr: "10.0.0.1:1234",
			want:       "5.6.7.8",
		},
		{
			name:       "XFF takes precedence over X-Real-IP",
			xff:        "1.2.3.4",
			xri:        "5.6.7.8",
			remoteAddr: "10.0.0.1:1234",
			want:       "1.2.3.4",
		},
		{
			name:       "no proxy headers falls back to RemoteAddr",
			remoteAddr: "10.0.0.1:1234",
			want:       "10.0.0.1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &http.Request{
				RemoteAddr: tt.remoteAddr,
				Header:     http.Header{},
			}
			if tt.xff != "" {
				r.Header.Set("X-Forwarded-For", tt.xff)
			}
			if tt.xri != "" {
				r.Header.Set("X-Real-IP", tt.xri)
			}
			got := ClientIP(r, true)
			if got != tt.want {
				t.Errorf("ClientIP(trustProxy=true) = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClientIPIgnoresHeadersWhenNotTrusted(t *testing.T) {
	r := &http.Request{
		RemoteAddr: "10.0.0.1:1234",
		Header:     http.Header{},
	}
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	r.Header.Set("X-Real-IP", "5.6.7.8")

	got := ClientIP(r, false)
	if got != "10.0.0.1" {
		t.Errorf("ClientIP(trustProxy=false) = %q, want %q (should ignore headers)", got, "10.0.0.1")
	}
}
