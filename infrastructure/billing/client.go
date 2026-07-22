// Package billing is an Anti-Corruption Layer client for ai-billing-service.
// The gateway acts as the portal's single entry point (BFF): the portal calls
// GET /usage on the gateway, and this client fetches the real cost + budget from
// the billing service and maps them into the portal's UsageMetrics contract.
//
// Base URL comes from BILLING_BASE_URL (default http://localhost:8084). When the
// billing service is unreachable the caller surfaces an error and the portal
// degrades gracefully to its static fallback (ADR-0003).
package billing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// Metrics mirrors the portal's UsageMetrics shape (ai-portal-frontend
// src/domain/types.ts): Token / QPS / Vector / Cost dimensions + provenance.
type Metrics struct {
	TokenBudget float64 `json:"tokenBudget"`
	TokenUsed   float64 `json:"tokenUsed"`
	QPSQuota    float64 `json:"qpsQuota"`
	QPSCurrent  float64 `json:"qpsCurrent"`
	VectorQuota float64 `json:"vectorQuota"`
	VectorUsed  float64 `json:"vectorUsed"`
	CostBudget  float64 `json:"costBudget"`
	CostActual  float64 `json:"costActual"`
	Source      string  `json:"source"`
}

// costResponse mirrors ai-billing-service CostResponse.
type costResponse struct {
	TenantID          string `json:"tenantId"`
	Period            string `json:"period"`
	UsageCostCents    int64  `json:"usageCostCents"`
	ResourceCostCents int64  `json:"resourceCostCents"`
	TotalCents        int64  `json:"totalCents"`
}

// budgetResponse mirrors ai-billing-service BudgetResponse.
type budgetResponse struct {
	TenantID    string  `json:"tenantId"`
	LimitCents  int64   `json:"limitCents"`
	SpentCents  int64   `json:"spentCents"`
	Utilization float64 `json:"utilization"`
	Alerted     bool    `json:"alerted"`
	Exceeded    bool    `json:"exceeded"`
}

// Client talks to ai-billing-service.
type Client struct {
	base string
	hc   *http.Client
}

// NewFromEnv builds a billing Client from BILLING_BASE_URL (default :8084).
func NewFromEnv() *Client {
	base := os.Getenv("BILLING_BASE_URL")
	if base == "" {
		base = "http://localhost:8084"
	}
	return &Client{base: base, hc: &http.Client{Timeout: 5 * time.Second}}
}

// Usage fetches cost + budget for a tenant and maps them to portal Metrics.
func (c *Client) Usage(ctx context.Context, tenantID string) (*Metrics, error) {
	var cost costResponse
	if err := c.getJSON(ctx, "/api/v1/tenants/"+tenantID+"/cost", tenantID, &cost); err != nil {
		return nil, fmt.Errorf("billing cost: %w", err)
	}
	var budget budgetResponse
	// Budget may be unset for a fresh tenant; treat a non-2xx as "no budget".
	_ = c.getJSON(ctx, "/api/v1/tenants/"+tenantID+"/budgets", tenantID, &budget)

	m := &Metrics{
		CostActual: float64(cost.TotalCents) / 100.0,
		CostBudget: float64(budget.LimitCents) / 100.0,
		// Token/QPS/Vector dimensions are not tracked by the billing service in
		// this build; report the cost dimension as authoritative and leave the
		// others at zero rather than fabricate values.
		Source: "billing",
	}
	return m, nil
}

// UsageEventInput is a single token-usage record to push to ai-billing-service.
type UsageEventInput struct {
	TenantID         string
	AppID            string
	Model            string
	PromptTokens     int
	CompletionTokens int
}

// ReportUsage pushes token usage to ai-billing-service (POST /api/v1/usage).
// It is best-effort: callers must not depend on it succeeding (billing may be
// down or multitenancy may be disabled). The reporter (metering sink) swallows
// the error and the chat hot path is never blocked.
func (c *Client) ReportUsage(ctx context.Context, ev UsageEventInput) error {
	now := time.Now().UnixNano()
	payload := []map[string]any{
		{
			"recordId": fmt.Sprintf("os-%d-in", now),
			"tenantId": ev.TenantID, "appId": ev.AppID, "model": ev.Model,
			"dimension": "TOKEN_INPUT", "amount": ev.PromptTokens,
		},
		{
			"recordId": fmt.Sprintf("os-%d-out", now),
			"tenantId": ev.TenantID, "appId": ev.AppID, "model": ev.Model,
			"dimension": "TOKEN_OUTPUT", "amount": ev.CompletionTokens,
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/v1/usage", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if ev.TenantID != "" {
		req.Header.Set("X-Tenant-Id", ev.TenantID)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("billing usage ingest status %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) getJSON(ctx context.Context, path, tenantID string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	// billing's AuthInterceptor requires a tenant identity (auth-contract §3):
	// either X-Tenant-Id or a bearer JWT. Forward the gateway-resolved tenant.
	if tenantID != "" {
		req.Header.Set("X-Tenant-Id", tenantID)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return json.Unmarshal(body, out)
}
