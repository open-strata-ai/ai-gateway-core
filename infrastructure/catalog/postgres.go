package catalog

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	_ "github.com/lib/pq"
	"github.com/open-strata-ai/ai-gateway-core/domain"
)

// Postgres is a PostgreSQL-backed ModelCatalog.
type Postgres struct {
	db *sql.DB
}

// NewPostgres opens a connection and ensures the model_catalog table exists.
func NewPostgres(dsn string) (*Postgres, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("catalog postgres: open: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("catalog postgres: ping: %w", err)
	}
	if _, err := db.Exec(migrateCatalog); err != nil {
		return nil, fmt.Errorf("catalog postgres: migrate: %w", err)
	}
	return &Postgres{db: db}, nil
}

const migrateCatalog = `
CREATE TABLE IF NOT EXISTS model_catalog (
    model_id         TEXT PRIMARY KEY,
    source           TEXT NOT NULL DEFAULT '',
    capability       TEXT NOT NULL DEFAULT '',
    context_window   INT NOT NULL DEFAULT 0,
    price_in         DOUBLE PRECISION NOT NULL DEFAULT 0,
    price_out        DOUBLE PRECISION NOT NULL DEFAULT 0,
    latency_sla_ms   INT NOT NULL DEFAULT 0,
    tps              INT NOT NULL DEFAULT 0,
    rate_limit       JSONB NOT NULL DEFAULT '{}',
    health           TEXT NOT NULL DEFAULT 'healthy',
    tenant_access    TEXT[] NOT NULL DEFAULT '{}',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);`

func rowToCard(scanner interface{ Scan(...interface{}) error }) (domain.ModelCard, error) {
	var card domain.ModelCard
	var rlJSON, tenantAccess string
	err := scanner.Scan(&card.ModelID, &card.Source, &card.Capability,
		&card.ContextWindow, &card.PriceIn, &card.PriceOut,
		&card.LatencySLA, &card.TPS, &rlJSON, &card.Health, &tenantAccess)
	if err != nil {
		return card, err
	}
	_ = json.Unmarshal([]byte(rlJSON), &card.RateLimit)
	if tenantAccess != "" && tenantAccess != "{}" {
		ta := strings.Trim(tenantAccess, "{}")
		card.TenantAccess = strings.Split(ta, ",")
	}
	return card, nil
}

func (p *Postgres) Get(modelID string) (domain.ModelCard, bool) {
	row := p.db.QueryRow(`SELECT model_id,source,capability,context_window,price_in,price_out,latency_sla_ms,tps,rate_limit,health,tenant_access FROM model_catalog WHERE model_id=$1`, modelID)
	card, err := rowToCard(row)
	if err != nil {
		return domain.ModelCard{}, false
	}
	return card, true
}

func (p *Postgres) ListByCapability(capability, tenantID string) []domain.ModelCard {
	rows, err := p.db.Query(`SELECT model_id,source,capability,context_window,price_in,price_out,latency_sla_ms,tps,rate_limit,health,tenant_access FROM model_catalog WHERE capability=$1 AND ($2='' OR $2=ANY(tenant_access)) ORDER BY model_id`, capability, tenantID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []domain.ModelCard
	for rows.Next() {
		card, err := rowToCard(rows)
		if err != nil {
			continue
		}
		out = append(out, card)
	}
	return out
}

func (p *Postgres) UpdateHealth(modelID string, h domain.HealthStatus) {
	_, _ = p.db.Exec(`UPDATE model_catalog SET health=$1, updated_at=NOW() WHERE model_id=$2`, h.State, modelID)
}

func (p *Postgres) Upsert(card domain.ModelCard) {
	if card.Health == "" {
		card.Health = domain.HealthHealthy
	}
	rlJSON, _ := json.Marshal(card.RateLimit)
	ta := "{}"
	if len(card.TenantAccess) > 0 {
		ta = "{" + strings.Join(card.TenantAccess, ",") + "}"
	}
	_, _ = p.db.Exec(`
		INSERT INTO model_catalog (model_id,source,capability,context_window,price_in,price_out,latency_sla_ms,tps,rate_limit,health,tenant_access)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10,$11::text[])
		ON CONFLICT (model_id) DO UPDATE SET
			source=EXCLUDED.source, capability=EXCLUDED.capability, context_window=EXCLUDED.context_window,
			price_in=EXCLUDED.price_in, price_out=EXCLUDED.price_out, latency_sla_ms=EXCLUDED.latency_sla_ms,
			tps=EXCLUDED.tps, rate_limit=EXCLUDED.rate_limit, health=EXCLUDED.health,
			tenant_access=EXCLUDED.tenant_access, updated_at=NOW()`,
		card.ModelID, card.Source, card.Capability, card.ContextWindow,
		card.PriceIn, card.PriceOut, card.LatencySLA, card.TPS, string(rlJSON),
		card.Health, ta,
	)
}

var _ domain.ModelCatalog = (*Postgres)(nil)
