// Package postgres implements the gateway's domain repositories
// (AgentRepository, SessionRepository) against PostgreSQL. They are the
// production, DB-backed counterparts of the offline in-memory stand-ins in
// infrastructure/repository/memory (DESIGN §8 / EU-04, EU-05). Tables are
// self-migrated on open so the gateway can boot against a fresh database.
package postgres

import (
	"database/sql"
	"encoding/json"
	"fmt"

	_ "github.com/lib/pq"

	"github.com/open-strata-ai/ai-gateway-core/domain"
)

// openDB connects, pings, and ensures both repository tables exist.
func openDB(dsn string) (*sql.DB, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres repo: open: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres repo: ping: %w", err)
	}
	if _, err := db.Exec(migrateAgentTable); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres repo: migrate agents: %w", err)
	}
	if _, err := db.Exec(migrateSessionTable); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres repo: migrate sessions: %w", err)
	}
	return db, nil
}

const migrateAgentTable = `
CREATE TABLE IF NOT EXISTS agents (
    id            TEXT PRIMARY KEY,
    tenant_id     TEXT NOT NULL DEFAULT '',
    name          TEXT NOT NULL DEFAULT '',
    description   TEXT NOT NULL DEFAULT '',
    model_binding JSONB,
    state_machine JSONB,
    guardrails    JSONB,
    status        TEXT NOT NULL DEFAULT 'draft',
    created_at    BIGINT NOT NULL DEFAULT 0,
    updated_at    BIGINT NOT NULL DEFAULT 0
);`

const migrateSessionTable = `
CREATE TABLE IF NOT EXISTS chat_sessions (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL DEFAULT '',
    agent_id   TEXT NOT NULL DEFAULT '',
    messages   JSONB NOT NULL DEFAULT '[]',
    created_at BIGINT NOT NULL DEFAULT 0
);`

// orEmpty returns the provided JSON string, or 'null' when empty so that
// jsonb columns receive a valid literal rather than an empty string.
func orEmpty(s string) string {
	if s == "" {
		return "null"
	}
	return s
}

// --- AgentRepository -------------------------------------------------------

// AgentRepository is a PostgreSQL-backed domain.AgentRepository.
type AgentRepository struct {
	db *sql.DB
}

// NewAgentRepository opens the connection and migrates the agents table.
func NewAgentRepository(dsn string) (*AgentRepository, error) {
	db, err := openDB(dsn)
	if err != nil {
		return nil, err
	}
	return &AgentRepository{db: db}, nil
}

func (r *AgentRepository) Save(a domain.AgentSpec) error {
	mb, _ := json.Marshal(a.ModelBinding)
	sm, _ := json.Marshal(a.StateMachine)
	gr, _ := json.Marshal(a.Guardrails)
	_, err := r.db.Exec(`
		INSERT INTO agents (id, tenant_id, name, description, model_binding, state_machine, guardrails, status, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5::jsonb,$6::jsonb,$7::jsonb,$8,$9,$10)
		ON CONFLICT (id) DO UPDATE SET
			tenant_id=EXCLUDED.tenant_id,
			name=EXCLUDED.name,
			description=EXCLUDED.description,
			model_binding=EXCLUDED.model_binding,
			state_machine=EXCLUDED.state_machine,
			guardrails=EXCLUDED.guardrails,
			status=EXCLUDED.status,
			updated_at=EXCLUDED.updated_at`,
		a.ID, a.TenantID, a.Name, a.Description,
		orEmpty(string(mb)), orEmpty(string(sm)), orEmpty(string(gr)),
		a.Status, a.CreatedAt, a.UpdatedAt,
	)
	return err
}

func (r *AgentRepository) Get(id string) (domain.AgentSpec, bool) {
	var a domain.AgentSpec
	var mb, sm, gr string
	row := r.db.QueryRow(`SELECT id, tenant_id, name, description, model_binding, state_machine, guardrails, status, created_at, updated_at FROM agents WHERE id=$1`, id)
	if err := row.Scan(&a.ID, &a.TenantID, &a.Name, &a.Description, &mb, &sm, &gr, &a.Status, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return domain.AgentSpec{}, false
	}
	_ = json.Unmarshal([]byte(orEmpty(mb)), &a.ModelBinding)
	_ = json.Unmarshal([]byte(orEmpty(sm)), &a.StateMachine)
	_ = json.Unmarshal([]byte(orEmpty(gr)), &a.Guardrails)
	return a, true
}

func (r *AgentRepository) List(tenantID string) []domain.AgentSpec {
	rows, err := r.db.Query(`SELECT id, tenant_id, name, description, model_binding, state_machine, guardrails, status, created_at, updated_at FROM agents WHERE ($1='' OR tenant_id=$1) ORDER BY updated_at DESC`, tenantID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]domain.AgentSpec, 0)
	for rows.Next() {
		var a domain.AgentSpec
		var mb, sm, gr string
		if err := rows.Scan(&a.ID, &a.TenantID, &a.Name, &a.Description, &mb, &sm, &gr, &a.Status, &a.CreatedAt, &a.UpdatedAt); err != nil {
			continue
		}
		_ = json.Unmarshal([]byte(orEmpty(mb)), &a.ModelBinding)
		_ = json.Unmarshal([]byte(orEmpty(sm)), &a.StateMachine)
		_ = json.Unmarshal([]byte(orEmpty(gr)), &a.Guardrails)
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil
	}
	return out
}

func (r *AgentRepository) Delete(id string) error {
	_, err := r.db.Exec(`DELETE FROM agents WHERE id=$1`, id)
	return err
}

var _ domain.AgentRepository = (*AgentRepository)(nil)

// --- SessionRepository -----------------------------------------------------

// SessionRepository is a PostgreSQL-backed domain.SessionRepository.
type SessionRepository struct {
	db *sql.DB
}

// NewSessionRepository opens the connection and migrates the chat_sessions table.
func NewSessionRepository(dsn string) (*SessionRepository, error) {
	db, err := openDB(dsn)
	if err != nil {
		return nil, err
	}
	return &SessionRepository{db: db}, nil
}

func (r *SessionRepository) Save(s domain.Session) error {
	msg, _ := json.Marshal(s.Messages)
	_, err := r.db.Exec(`
		INSERT INTO chat_sessions (id, tenant_id, agent_id, messages, created_at)
		VALUES ($1,$2,$3,$4::jsonb,$5)
		ON CONFLICT (id) DO UPDATE SET
			tenant_id=EXCLUDED.tenant_id,
			agent_id=EXCLUDED.agent_id,
			messages=EXCLUDED.messages`,
		s.ID, s.TenantID, s.AgentID, orEmpty(string(msg)), s.CreatedAt,
	)
	return err
}

func (r *SessionRepository) Get(id string) (domain.Session, bool) {
	var s domain.Session
	var msg string
	row := r.db.QueryRow(`SELECT id, tenant_id, agent_id, messages, created_at FROM chat_sessions WHERE id=$1`, id)
	if err := row.Scan(&s.ID, &s.TenantID, &s.AgentID, &msg, &s.CreatedAt); err != nil {
		return domain.Session{}, false
	}
	_ = json.Unmarshal([]byte(orEmpty(msg)), &s.Messages)
	return s, true
}

func (r *SessionRepository) AppendMessage(id string, m domain.Message) error {
	// Read-modify-write: load current messages, append, persist atomically.
	var raw string
	row := r.db.QueryRow(`SELECT messages FROM chat_sessions WHERE id=$1`, id)
	if err := row.Scan(&raw); err != nil {
		return domain.NewError(domain.ErrInvalidRequest, 404, "session not found")
	}
	var msgs []domain.Message
	if err := json.Unmarshal([]byte(orEmpty(raw)), &msgs); err != nil {
		msgs = []domain.Message{}
	}
	msgs = append(msgs, m)
	out, _ := json.Marshal(msgs)
	_, err := r.db.Exec(`UPDATE chat_sessions SET messages=$1::jsonb WHERE id=$2`, orEmpty(string(out)), id)
	return err
}

func (r *SessionRepository) ListByTenant(tenantID string) []domain.Session {
	rows, err := r.db.Query(`SELECT id, tenant_id, agent_id, messages, created_at FROM chat_sessions WHERE tenant_id=$1 ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]domain.Session, 0)
	for rows.Next() {
		var s domain.Session
		var msg string
		if err := rows.Scan(&s.ID, &s.TenantID, &s.AgentID, &msg, &s.CreatedAt); err != nil {
			continue
		}
		_ = json.Unmarshal([]byte(orEmpty(msg)), &s.Messages)
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil
	}
	return out
}

var _ domain.SessionRepository = (*SessionRepository)(nil)
