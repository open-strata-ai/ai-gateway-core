// Package config loads gateway configuration (DESIGN §7.4 / §11.5). To keep the
// build stdlib-only it ships typed defaults and an optional JSON overlay;
// production renders infrastructure/config/gateway.yaml via the meta repository.
package config

import (
	"encoding/json"
	"os"
)

// Config mirrors the DESIGN §11.5 config keys.
type Config struct {
	Gateway struct {
		Listen   string `json:"listen"`
		Upstream string `json:"upstream"`
	} `json:"gateway"`
	ModelRouting struct {
		Default       string   `json:"default"`
		FallbackChain []string `json:"fallbackChain"`
		CostAware     bool     `json:"costAware"`
	} `json:"modelRouting"`
	RateLimit struct {
		Backend             string `json:"backend"`
		DefaultQPSPerTenant int    `json:"defaultQPSPerTenant"`
		DefaultTPMPerTenant int    `json:"defaultTPMPerTenant"`
		GlobalQPS           int    `json:"globalQPS"`
	} `json:"ratelimit"`
	CircuitBreaker struct {
		ErrorThreshold float64 `json:"errorThreshold"`
		CooldownMs     int     `json:"cooldownMs"`
	} `json:"circuitBreaker"`
	Cache struct {
		Enabled           bool    `json:"enabled"`
		SemanticThreshold float64 `json:"semanticThreshold"`
	} `json:"cache"`
	Egress struct {
		PIIScan           bool     `json:"piiScan"`
		DenyEgressTenants []string `json:"denyEgressTenants"`
	} `json:"egress"`
}

// Default returns the DESIGN §11.5 defaults.
func Default() Config {
	var c Config
	c.Gateway.Listen = "0.0.0.0:8080"
	c.Gateway.Upstream = "higress://ai-system"
	c.ModelRouting.Default = "cloud-qwen-max"
	c.ModelRouting.FallbackChain = []string{"cloud-gpt-4o"}
	c.ModelRouting.CostAware = true
	c.RateLimit.Backend = "redis"
	c.RateLimit.DefaultQPSPerTenant = 20
	c.RateLimit.DefaultTPMPerTenant = 200000
	c.RateLimit.GlobalQPS = 0
	c.CircuitBreaker.ErrorThreshold = 0.5
	c.CircuitBreaker.CooldownMs = 30000
	c.Cache.Enabled = false
	c.Cache.SemanticThreshold = 0.95
	c.Egress.PIIScan = true
	c.Egress.DenyEgressTenants = []string{}
	return c
}

// Load reads a JSON overlay from path and merges it onto the defaults. A missing
// path returns the defaults unchanged.
func Load(path string) (Config, error) {
	c := Default()
	if path == "" {
		return c, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, err
	}
	return c, nil
}
