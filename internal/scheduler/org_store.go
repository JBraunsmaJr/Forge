package scheduler

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/JBraunsmaJr/forge/internal/api"
	"github.com/JBraunsmaJr/forge/internal/store"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// OrgStore manages orgs and their policies in Postgres.
type OrgStore struct {
	db  *sql.DB
	gdb *gorm.DB
}

func newOrgStore(db *sql.DB, gdb *gorm.DB) *OrgStore {
	return &OrgStore{db: db, gdb: gdb}
}

func (o *OrgStore) CreateOrg(name string) (*api.OrgInfo, error) {
	org := store.Org{
		ID:   newID()[:12],
		Name: name,
	}
	err := o.gdb.Create(&org).Error
	if err != nil {
		if err == gorm.ErrDuplicatedKey {
			var existing store.Org
			if o.gdb.Where("name = ?", name).First(&existing).Error == nil {
				return nil, fmt.Errorf("org %q already exists", name)
			}
		}
		return nil, err
	}
	return &api.OrgInfo{ID: org.ID, Name: org.Name, CreatedAt: org.CreatedAt}, nil
}

func (o *OrgStore) ListOrgs() []api.OrgInfo {
	var orgs []store.Org
	o.gdb.Order("created_at").Find(&orgs)
	result := make([]api.OrgInfo, len(orgs))
	for i, org := range orgs {
		result[i] = api.OrgInfo{ID: org.ID, Name: org.Name, CreatedAt: org.CreatedAt}
	}
	return result
}

func (o *OrgStore) GetOrg(orgID string) (*api.OrgInfo, bool) {
	var org store.Org
	if err := o.gdb.First(&org, "id = ?", orgID).Error; err != nil {
		return nil, false
	}
	return &api.OrgInfo{ID: org.ID, Name: org.Name, CreatedAt: org.CreatedAt}, true
}

func (o *OrgStore) CreatePolicy(orgID string, req api.CreatePolicyRequest) (*api.PolicyInfo, error) {
	// Verify org exists.
	var org store.Org
	if err := o.gdb.First(&org, "id = ?", orgID).Error; err != nil {
		return nil, fmt.Errorf("org %s not found", orgID)
	}

	stepsJSON, _ := json.Marshal(req.Steps)
	var transformerJSON []byte
	if req.Transformer != nil {
		if req.Transformer.Image == "" && req.Transformer.Script == "" {
			return nil, fmt.Errorf("transformer must specify either 'image' or 'script'")
		}
		transformerJSON, _ = json.Marshal(req.Transformer)
	}

	policy := store.Policy{
		ID:             newID()[:12],
		OrgID:          orgID,
		Name:           req.Name,
		Description:    req.Description,
		Steps:          datatypes.JSON(stepsJSON),
		Transformer:    datatypes.JSON(transformerJSON),
		ForbidOverride: req.ForbidOverride,
	}

	if err := o.gdb.Create(&policy).Error; err != nil {
		return nil, fmt.Errorf("inserting policy: %w", err)
	}

	return &api.PolicyInfo{
		ID:             policy.ID,
		OrgID:          orgID,
		Name:           policy.Name,
		Description:    policy.Description,
		Steps:          req.Steps,
		Transformer:    req.Transformer,
		ForbidOverride: policy.ForbidOverride,
		CreatedAt:      policy.CreatedAt,
	}, nil
}

func (o *OrgStore) GetPolicies(orgID string) ([]api.PolicyInfo, bool) {
	// Verify org exists.
	var org store.Org
	if err := o.gdb.First(&org, "id = ?", orgID).Error; err != nil {
		return nil, false
	}

	var policies []store.Policy
	if err := o.gdb.Where("org_id = ?", orgID).Order("created_at").Find(&policies).Error; err != nil {
		return nil, false
	}

	result := make([]api.PolicyInfo, len(policies))
	for i, p := range policies {
		info := api.PolicyInfo{
			ID:             p.ID,
			OrgID:          orgID,
			Name:           p.Name,
			Description:    p.Description,
			ForbidOverride: p.ForbidOverride,
			CreatedAt:      p.CreatedAt,
		}
		_ = json.Unmarshal(p.Steps, &info.Steps)
		if len(p.Transformer) > 0 && string(p.Transformer) != "null" {
			var t api.PolicyTransformer
			if err := json.Unmarshal(p.Transformer, &t); err == nil {
				if t.Image != "" || t.Script != "" {
					info.Transformer = &t
				}
			}
		}
		result[i] = info
	}
	return result, true
}

func (o *OrgStore) DeletePolicy(orgID, policyID string) error {
	result := o.gdb.Where("id = ? AND org_id = ?", policyID, orgID).Delete(&store.Policy{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("policy %s not found in org %s", policyID, orgID)
	}
	return nil
}

func (o *OrgStore) UpdatePolicy(orgID, policyID string, req api.UpdatePolicyRequest) (*api.PolicyInfo, error) {
	stepsJSON, _ := json.Marshal(req.Steps)
	var transformerJSON []byte
	if req.Transformer != nil {
		if req.Transformer.Image == "" && req.Transformer.Script == "" {
			return nil, fmt.Errorf("transformer must specify either 'image' or 'script'")
		}
		transformerJSON, _ = json.Marshal(req.Transformer)
	}

	var policy store.Policy
	if err := o.gdb.First(&policy, "id = ? AND org_id = ?", policyID, orgID).Error; err != nil {
		return nil, fmt.Errorf("policy %s not found in org %s", policyID, orgID)
	}

	policy.Name = req.Name
	policy.Description = req.Description
	policy.Steps = datatypes.JSON(stepsJSON)
	policy.Transformer = datatypes.JSON(transformerJSON)
	policy.ForbidOverride = req.ForbidOverride

	if err := o.gdb.Save(&policy).Error; err != nil {
		return nil, fmt.Errorf("updating policy: %w", err)
	}

	return &api.PolicyInfo{
		ID:             policyID,
		OrgID:          orgID,
		Name:           policy.Name,
		Description:    policy.Description,
		Steps:          req.Steps,
		Transformer:    req.Transformer,
		ForbidOverride: policy.ForbidOverride,
		CreatedAt:      policy.CreatedAt,
	}, nil
}
