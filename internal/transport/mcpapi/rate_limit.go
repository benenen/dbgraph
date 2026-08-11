package mcpapi

import (
	"crypto/sha256"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/benenen/dbgraph/internal/relations"
)

type toolClass byte

const (
	toolCheapRead toolClass = iota + 1
	toolExpensiveRead
	toolWrite
	toolAuthentication
	toolProtocol
)

type mcpRateLimits struct {
	cheap          int
	expensive      int
	writes         int
	authentication int
	maximumKeys    int
}

func defaultMCPRateLimits() mcpRateLimits {
	return mcpRateLimits{cheap: 300, expensive: 60, writes: 30, authentication: 20, maximumKeys: 10_000}
}

type mcpRateWindow struct {
	started time.Time
	count   int
}

type mcpRateLimiter struct {
	mu          sync.Mutex
	windows     map[[sha256.Size]byte]mcpRateWindow
	limits      mcpRateLimits
	lastCleanup time.Time
}

func newMCPRateLimiter(limits mcpRateLimits) *mcpRateLimiter {
	return &mcpRateLimiter{windows: make(map[[sha256.Size]byte]mcpRateWindow), limits: limits}
}

func (l *mcpRateLimiter) Allow(principal relations.Principal, ip string, class toolClass, now time.Time) bool {
	keys := [][sha256.Size]byte{rateKey("ip", ip, class)}
	if principal.Actor != "" && principal.Actor != "anonymous" {
		identity := fmt.Sprintf("%d:%d:%s", principal.Role, principal.Origin, principal.Actor)
		keys = append(keys, rateKey("principal", identity, class))
	}
	return l.allowKeys(keys, l.limit(class), now)
}

func (l *mcpRateLimiter) AllowAuthentication(ip string, now time.Time) bool {
	return l.allowKeys([][sha256.Size]byte{rateKey("authentication", ip, toolAuthentication)}, l.limit(toolAuthentication), now)
}

func (l *mcpRateLimiter) AllowProtocol(principal relations.Principal, ip string, now time.Time) bool {
	return l.Allow(principal, ip, toolProtocol, now)
}

func (l *mcpRateLimiter) allowKeys(keys [][sha256.Size]byte, limit int, now time.Time) bool {
	if l == nil || limit <= 0 {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lastCleanup.IsZero() || now.Sub(l.lastCleanup) >= time.Minute {
		for key, window := range l.windows {
			if now.Sub(window.started) >= 5*time.Minute {
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
		if window.count >= limit {
			return false
		}
	}
	if len(l.windows)+newKeys > l.limits.maximumKeys {
		return false
	}
	for _, key := range keys {
		window, exists := l.windows[key]
		if !exists || now.Sub(window.started) >= time.Minute {
			l.windows[key] = mcpRateWindow{started: now, count: 1}
			continue
		}
		window.count++
		l.windows[key] = window
	}
	return true
}

func (l *mcpRateLimiter) limit(class toolClass) int {
	switch class {
	case toolCheapRead:
		return l.limits.cheap
	case toolExpensiveRead:
		return l.limits.expensive
	case toolWrite:
		return l.limits.writes
	case toolAuthentication:
		return l.limits.authentication
	case toolProtocol:
		return l.limits.cheap
	default:
		return 0
	}
}

func classifyTool(name string) toolClass {
	switch name {
	case "dbgraph_trace", "dbgraph_impact", "dbgraph_search_nodes", "dbgraph_get_relation",
		"dbgraph_explain_relation", "dbgraph_get_relation_init", "dbgraph_list_proposals",
		"dbgraph_list_unresolved":
		return toolExpensiveRead
	case "dbgraph_propose_relation", "dbgraph_begin_relation_init", "dbgraph_propose_relations",
		"dbgraph_complete_relation_init", "dbgraph_propose_relation_revision",
		"dbgraph_propose_relation_tombstone", "dbgraph_review_relation",
		"dbgraph_suppress_relation", "dbgraph_restore_relation", "dbgraph_start_schema_scan":
		return toolWrite
	default:
		return toolCheapRead
	}
}

func rateKey(scope string, value string, class toolClass) [sha256.Size]byte {
	return sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s", scope, class, value)))
}

func normalizedClientIP(remoteAddress string) string {
	if address, err := netip.ParseAddr(remoteAddress); err == nil {
		return address.Unmap().String()
	}
	host, _, err := net.SplitHostPort(remoteAddress)
	if err != nil {
		return "unknown"
	}
	address, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return "unknown"
	}
	return address.Unmap().String()
}
