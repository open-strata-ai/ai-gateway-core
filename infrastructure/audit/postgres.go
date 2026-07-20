package audit

import (
	"database/sql"
	"fmt"

	_ "github.com/lib/pq"
	"github.com/open-strata-ai/ai-gateway-core/domain"
)

// Postgres stores audit entries in a PostgreSQL immutable audit_log table.
type Postgres struct {
	db *sql.DB
}

// NewPostgres opens a connection and ensures the audit_log table exists.
func NewPostgres(dsn string) (*Postgres, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("audit postgres: open: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("audit postgres: ping: %w", err)
	}
	if _, err := db.Exec(migrateAudit); err != nil {
		return nil, fmt.Errorf("audit postgres: migrate: %w", err)
	}
	return &Postgres{db: db}, nil
}

const migrateAudit = `
CREATE TABLE IF NOT EXISTS audit_log (
    id         BIGSERIAL PRIMARY KEY,
    tenant_id  TEXT NOT NULL DEFAULT '',
    action     TEXT NOT NULL DEFAULT '',
    model      TEXT NOT NULL DEFAULT '',
    outcome    TEXT NOT NULL DEFAULT '',
    detail     TEXT NOT NULL DEFAULT '',
    unix_nanos BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);`

func (p *Postgres) Append(e domain.AuditEntry) {
	_, _ = p.db.Exec(
		`INSERT INTO audit_log (tenant_id, action, model, outcome, detail, unix_nanos) VALUES ($1,$2,$3,$4,$5,$6)`,
		e.TenantID, e.Action, e.Model, e.Outcome, e.Detail, e.UnixNanos,
	)
}

var _ domain.AuditRecorder = (*Postgres)(nil)
