// Package scheduler — Postgres-backed org and policy store.
package scheduler

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/JBraunsmaJr/forge/internal/api"
)

// OrgStore manages orgs and their policies in Postgres.
type OrgStore struct {
	db *sql.DB
}

func newOrgStore(db *sql.DB) *OrgStore {
	return &OrgStore{db: db}
}

func (o *OrgStore) CreateOrg(name string) (*api.OrgInfo, error) {
	id := newID()[:12]
	var createdAt time.Time
	err := o.db.QueryRow(
		`INSERT INTO orgs (id, name) VALUES ($1, $2)
		 ON CONFLICT (name) DO NOTHING
		 RETURNING created_at`,
		id, name,
	).Scan(&createdAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("org %q already exists", name)
	}
	if err != nil {
		return nil, err
	}
	return &api.OrgInfo{ID: id, Name: name, CreatedAt: createdAt}, nil
}

func (o *OrgStore) ListOrgs() []api.OrgInfo {
	rows, err := o.db.Query(`SELECT id, name, created_at FROM orgs ORDER BY created_at`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var result []api.OrgInfo
	for rows.Next() {
		var org api.OrgInfo
		rows.Scan(&org.ID, &org.Name, &org.CreatedAt)
		result = append(result, org)
	}
	return result
}

func (o *OrgStore) GetOrg(orgID string) (*api.OrgInfo, bool) {
	var org api.OrgInfo
	err := o.db.QueryRow(
		`SELECT id, name, created_at FROM orgs WHERE id=$1`, orgID,
	).Scan(&org.ID, &org.Name, &org.CreatedAt)
	if err != nil {
		return nil, false
	}
	return &org, true
}

func (o *OrgStore) CreatePolicy(orgID string, req api.CreatePolicyRequest) (*api.PolicyInfo, error) {
	// Verify org exists.
	var exists bool
	o.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM orgs WHERE id=$1)`, orgID).Scan(&exists)
	if !exists {
		return nil, fmt.Errorf("org %s not found", orgID)
	}

	id := newID()[:12]
	stepsJSON, _ := json.Marshal(req.Steps)
	var transformerJSON []byte
	if req.Transformer != nil {
		transformerJSON, _ = json.Marshal(req.Transformer)
	}

	var createdAt time.Time
	err := o.db.QueryRow(`
		INSERT INTO policies (id, org_id, name, description, steps, transformer, forbid_override)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		RETURNING created_at`,
		id, orgID, req.Name, req.Description, stepsJSON, transformerJSON, req.ForbidOverride,
	).Scan(&createdAt)
	if err != nil {
		return nil, fmt.Errorf("inserting policy: %w", err)
	}

	return &api.PolicyInfo{
		ID:             id,
		OrgID:          orgID,
		Name:           req.Name,
		Description:    req.Description,
		Steps:          req.Steps,
		Transformer:    req.Transformer,
		ForbidOverride: req.ForbidOverride,
		CreatedAt:      createdAt,
	}, nil
}

func (o *OrgStore) GetPolicies(orgID string) ([]api.PolicyInfo, bool) {
	var exists bool
	o.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM orgs WHERE id=$1)`, orgID).Scan(&exists)
	if !exists {
		return nil, false
	}

	rows, err := o.db.Query(`
		SELECT id, name, description, steps, transformer, forbid_override, created_at
		FROM   policies WHERE org_id=$1 ORDER BY created_at`, orgID)
	if err != nil {
		return nil, false
	}
	defer rows.Close()

	var result []api.PolicyInfo
	for rows.Next() {
		var p api.PolicyInfo
		var stepsJSON []byte
		var transformerJSON sql.NullString
		rows.Scan(&p.ID, &p.Name, &p.Description, &stepsJSON,
			&transformerJSON, &p.ForbidOverride, &p.CreatedAt)
		json.Unmarshal(stepsJSON, &p.Steps)
		if transformerJSON.Valid && transformerJSON.String != "" {
			var t api.PolicyTransformer
			if err := json.Unmarshal([]byte(transformerJSON.String), &t); err == nil {
				p.Transformer = &t
			}
		}
		p.OrgID = orgID
		result = append(result, p)
	}
	return result, true
}

func (o *OrgStore) DeletePolicy(orgID, policyID string) error {
	res, err := o.db.Exec(
		`DELETE FROM policies WHERE id=$1 AND org_id=$2`, policyID, orgID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("policy %s not found in org %s", policyID, orgID)
	}
	return nil
}
