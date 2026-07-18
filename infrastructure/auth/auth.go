// Package auth is a domain.AuthPort stub. In production Higress verifies the
// Keycloak JWT and this port re-derives tenant_id/role from the verified claims
// (auth-contract.md); offline it trusts the X-Tenant-Id header for local runs.
package auth

import (
	"context"
	"errors"
	"strings"

	"github.com/open-strata-ai/ai-gateway-core/domain"
)

// Stub resolves tenant context without a real Keycloak dependency.
type Stub struct {
	// DevTenant is used when no header/token is present (local dev only).
	DevTenant string
}

// New builds an auth stub.
func New(devTenant string) *Stub { return &Stub{DevTenant: devTenant} }

// Resolve maps a bearer token / X-Tenant-Id header to tenant_id + role.
func (s *Stub) Resolve(ctx context.Context, bearer, tenantHeader string) (string, string, error) {
	if tenantHeader != "" {
		return tenantHeader, roleFromBearer(bearer), nil
	}
	// Very small offline convenience: accept "Bearer tenant:role" form.
	if t, r, ok := parseDevBearer(bearer); ok {
		return t, r, nil
	}
	if s.DevTenant != "" {
		return s.DevTenant, "member", nil
	}
	return "", "", errors.New("missing tenant context")
}

func roleFromBearer(bearer string) string {
	if _, r, ok := parseDevBearer(bearer); ok {
		return r
	}
	return "member"
}

func parseDevBearer(bearer string) (string, string, bool) {
	b := strings.TrimSpace(strings.TrimPrefix(bearer, "Bearer "))
	if b == "" {
		return "", "", false
	}
	parts := strings.SplitN(b, ":", 2)
	if len(parts) == 2 && parts[0] != "" {
		return parts[0], parts[1], true
	}
	return "", "", false
}

var _ domain.AuthPort = (*Stub)(nil)
