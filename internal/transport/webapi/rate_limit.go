package webapi

import (
	"crypto/sha256"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/benenen/dbgraph/internal/relations"
)

const maximumWritesPerMinute = 30

const maximumExpensiveReadsPerMinute = 30

const maximumRateLimitSessions = 10_000

const maximumLoginAttemptsPerMinute = 10

type rateWindow struct {
	started time.Time
	count   int
}

type principalIPLimiter struct {
	mu          sync.Mutex
	windows     map[[sha256.Size]byte]rateWindow
	lastCleanup time.Time
}

type loginLimiter struct {
	mu          sync.Mutex
	windows     map[[sha256.Size]byte]rateWindow
	lastCleanup time.Time
}

func newPrincipalIPLimiter() *principalIPLimiter {
	return &principalIPLimiter{windows: make(map[[sha256.Size]byte]rateWindow)}
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{windows: make(map[[sha256.Size]byte]rateWindow)}
}

func (l *loginLimiter) Allow(clientIP string, credential string, now time.Time) bool {
	keys := [][sha256.Size]byte{
		sha256.Sum256([]byte("ip:" + clientIP)),
		sha256.Sum256([]byte("credential:" + credential)),
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lastCleanup.IsZero() || now.Sub(l.lastCleanup) >= time.Minute {
		for key, existing := range l.windows {
			if now.Sub(existing.started) >= 5*time.Minute {
				delete(l.windows, key)
			}
		}
		l.lastCleanup = now
	}
	newKeys := 0
	for _, key := range keys {
		window, exists := l.windows[key]
		if !exists || now.Sub(window.started) >= time.Minute {
			if !exists {
				newKeys++
			}
			continue
		}
		if window.count >= maximumLoginAttemptsPerMinute {
			return false
		}
	}
	if len(l.windows)+newKeys > maximumRateLimitSessions {
		return false
	}
	for _, key := range keys {
		window, exists := l.windows[key]
		if !exists || now.Sub(window.started) >= time.Minute {
			l.windows[key] = rateWindow{started: now, count: 1}
			continue
		}
		window.count++
		l.windows[key] = window
	}
	return true
}

func webClientIP(remoteAddress string) string {
	host, _, err := net.SplitHostPort(remoteAddress)
	if err != nil {
		host = remoteAddress
	}
	address, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return "unknown"
	}
	return address.Unmap().String()
}

func (l *principalIPLimiter) Allow(principal relations.Principal, clientIP string, now time.Time, maximum int) bool {
	keys := [][sha256.Size]byte{
		sha256.Sum256([]byte(
			"principal:" + strconv.Itoa(int(principal.Role)) + ":" + strconv.Itoa(int(principal.Origin)) + ":" + principal.Actor,
		)),
		sha256.Sum256([]byte("ip:" + clientIP)),
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lastCleanup.IsZero() || now.Sub(l.lastCleanup) >= time.Minute {
		for digest, existing := range l.windows {
			if now.Sub(existing.started) >= 5*time.Minute {
				delete(l.windows, digest)
			}
		}
		l.lastCleanup = now
	}
	newKeys := 0
	for _, key := range keys {
		window, exists := l.windows[key]
		if !exists || now.Sub(window.started) >= time.Minute {
			if !exists {
				newKeys++
			}
			continue
		}
		if window.count >= maximum {
			return false
		}
	}
	if len(l.windows)+newKeys > maximumRateLimitSessions {
		return false
	}
	for _, key := range keys {
		window, exists := l.windows[key]
		if !exists || now.Sub(window.started) >= time.Minute {
			l.windows[key] = rateWindow{started: now, count: 1}
			continue
		}
		window.count++
		l.windows[key] = window
	}
	return true
}
