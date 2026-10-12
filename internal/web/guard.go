package web

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

func clientIP(r *http.Request, trustForwarded bool) string {
	if trustForwarded {
		if lines := r.Header.Values("X-Forwarded-For"); len(lines) > 0 {
			parts := strings.Split(lines[len(lines)-1], ",")
			if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// limitKey is the rate limiter's bucket for a client address. An IPv6 client
// usually holds a whole /64, so it is limited as one; IPv4, including
// IPv4-mapped IPv6, is limited per address. Anything unparseable is its own key.
func limitKey(addr string) string {
	ip := net.ParseIP(addr)
	if ip == nil {
		return addr
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

type bucket struct {
	l    *rate.Limiter
	seen time.Time
}

type limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	last    time.Time
}

func newLimiter() *limiter { return &limiter{buckets: map[string]*bucket{}, last: time.Now()} }

func (l *limiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if now.Sub(l.last) > time.Minute {
		for k, b := range l.buckets {
			if now.Sub(b.seen) > 10*time.Minute {
				delete(l.buckets, k)
			}
		}
		l.last = now
	}
	b, ok := l.buckets[ip]
	if !ok {
		b = &bucket{l: rate.NewLimiter(rate.Every(6*time.Second), 20)}
		l.buckets[ip] = b
	}
	b.seen = now
	return b.l.Allow()
}
