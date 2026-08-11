package mcpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	appauth "github.com/benenen/dbgraph/internal/auth"
	"github.com/benenen/dbgraph/internal/relations"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const maximumMCPRequestBytes = 1 << 20

type mcpContextKey byte

const mcpIdentityContextKey mcpContextKey = 1

type mcpRequestIdentity struct {
	principal relations.Principal
	clientIP  string
	limiter   *mcpRateLimiter
	now       func() time.Time
}

type HTTPOptions struct {
	Now    func() time.Time
	limits mcpRateLimits
}

func NewHTTPHandler(services Services, authenticator *appauth.TokenAuthenticator) http.Handler {
	return NewHTTPHandlerWithOptions(services, authenticator, HTTPOptions{})
}

func NewHTTPHandlerWithOptions(
	services Services,
	authenticator *appauth.TokenAuthenticator,
	options HTTPOptions,
) http.Handler {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.limits.maximumKeys == 0 {
		options.limits = defaultMCPRateLimits()
	}
	limiter := newMCPRateLimiter(options.limits)
	streamable := mcp.NewStreamableHTTPHandler(func(request *http.Request) *mcp.Server {
		identity, ok := request.Context().Value(mcpIdentityContextKey).(mcpRequestIdentity)
		if !ok {
			identity = mcpRequestIdentity{principal: ViewerPrincipal(), clientIP: "unknown", limiter: limiter, now: options.Now}
		}
		return NewServer(services, identity.principal)
	}, &mcp.StreamableHTTPOptions{
		Stateless:                    true,
		JSONResponse:                 true,
		MaxRequestBodyBytes:          maximumMCPRequestBytes,
		PropagateRequestCancellation: true,
	})

	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		clientIP := normalizedClientIP(request.RemoteAddr)
		principal := ViewerPrincipal()
		if request.Header.Get("Authorization") == "" && authenticator.HasCredentials() {
			response.Header().Set("WWW-Authenticate", `Bearer realm="dbgraph"`)
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return
		}
		if request.Header.Get("Authorization") != "" {
			authenticated, ok := authenticator.Authenticate(request)
			if !ok {
				if !limiter.AllowAuthentication(clientIP, options.Now()) {
					response.Header().Set("Retry-After", "60")
					http.Error(response, "too many authentication attempts", http.StatusTooManyRequests)
					return
				}
				response.Header().Set("WWW-Authenticate", `Bearer realm="dbgraph"`)
				http.Error(response, "unauthorized", http.StatusUnauthorized)
				return
			}
			principal = authenticated
		}
		if !limiter.AllowProtocol(principal, clientIP, options.Now()) {
			writeProtocolRateLimit(response)
			return
		}
		identity := mcpRequestIdentity{principal: principal, clientIP: clientIP, limiter: limiter, now: options.Now}
		ctx := context.WithValue(request.Context(), mcpIdentityContextKey, identity)
		streamable.ServeHTTP(response, request.WithContext(ctx))
	})
}

func writeProtocolRateLimit(response http.ResponseWriter) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Retry-After", "60")
	response.WriteHeader(http.StatusTooManyRequests)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      nil,
		"error": map[string]any{
			"code": -32000, "message": "too many requests",
		},
	})
}
