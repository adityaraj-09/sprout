package branch

import (
	"context"
	"fmt"
	"strings"

	"github.com/adityaraj/sprout/internal/auth"
	"github.com/adityaraj/sprout/internal/meta"
)

func (s *Service) EnsureDefaultOrg(ctx context.Context) (meta.Org, error) {
	login := auth.OwnerFrom(ctx)
	if login == "" {
		return meta.Org{}, fmt.Errorf("forbidden: machine token has no personal org")
	}
	return s.Store.EnsureDefaultOrg(ctx, login)
}

func (s *Service) CreateOrg(ctx context.Context, name string) (meta.Org, error) {
	login := auth.OwnerFrom(ctx)
	if login == "" {
		return meta.Org{}, fmt.Errorf("forbidden: GitHub login required to create an org")
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if !nameRe.MatchString(name) {
		return meta.Org{}, fmt.Errorf("invalid_name: org name must match [a-z][a-z0-9-]*")
	}
	return s.Store.CreateOrg(ctx, login, name)
}

func (s *Service) ListOrgs(ctx context.Context) ([]meta.Org, error) {
	return s.Store.ListOrgs(ctx, auth.OwnerFrom(ctx))
}

func (s *Service) ResolveOrg(ctx context.Context, nameOrID string) (meta.Org, error) {
	nameOrID = strings.TrimSpace(nameOrID)
	if nameOrID == "" {
		nameOrID = meta.DefaultOrg
	}
	login := auth.OwnerFrom(ctx)
	if id := strings.ToLower(nameOrID); len(id) >= 8 {
		if o, err := s.Store.GetOrg(ctx, nameOrID); err == nil {
			if login == "" {
				return o, nil
			}
			if _, err := s.Store.GetOrgMember(ctx, o.ID, login); err == nil {
				return o, nil
			}
			return meta.Org{}, fmt.Errorf("forbidden: not a member of org %q", o.Name)
		}
	}
	list, err := s.Store.ListOrgs(ctx, login)
	if err != nil {
		return meta.Org{}, err
	}
	want := strings.ToLower(nameOrID)
	var matches []meta.Org
	for _, o := range list {
		if strings.EqualFold(o.Name, want) || o.ID == nameOrID {
			matches = append(matches, o)
		}
	}
	if login != "" && want == meta.DefaultOrg {
		var owned []meta.Org
		for _, o := range matches {
			if o.CreatedBy == login && o.Name == meta.DefaultOrg {
				owned = append(owned, o)
			}
		}
		if len(owned) == 1 {
			return owned[0], nil
		}
	}
	switch len(matches) {
	case 0:
		return meta.Org{}, fmt.Errorf("org_not_found: %q", nameOrID)
	case 1:
		return matches[0], nil
	default:
		return meta.Org{}, fmt.Errorf("ambiguous_org: %q matches more than one org — pass the org id", nameOrID)
	}
}

func (s *Service) ListOrgMembers(ctx context.Context, nameOrID string) ([]meta.OrgMember, error) {
	org, err := s.requireOrgMemberNamed(ctx, nameOrID)
	if err != nil {
		return nil, err
	}
	return s.Store.ListOrgMembers(ctx, org.ID)
}

func (s *Service) AddOrgMember(ctx context.Context, nameOrID, login string) (meta.OrgMember, error) {
	org, err := s.requireOrgOwnerNamed(ctx, nameOrID)
	if err != nil {
		return meta.OrgMember{}, err
	}
	login = strings.ToLower(strings.TrimSpace(login))
	if login == "" {
		return meta.OrgMember{}, fmt.Errorf("invalid_name: GitHub login required")
	}
	m := meta.OrgMember{
		OrgID: org.ID, Login: login, Role: meta.OrgRoleMember, AddedBy: auth.OwnerFrom(ctx),
	}
	if err := s.Store.AddOrgMember(ctx, m); err != nil {
		return meta.OrgMember{}, err
	}
	return s.Store.GetOrgMember(ctx, org.ID, login)
}

func (s *Service) RemoveOrgMember(ctx context.Context, nameOrID, login string) error {
	org, err := s.requireOrgOwnerNamed(ctx, nameOrID)
	if err != nil {
		return err
	}
	login = strings.ToLower(strings.TrimSpace(login))
	if login == org.CreatedBy {
		return fmt.Errorf("forbidden: cannot remove the org owner")
	}
	members, err := s.Store.ListOrgMembers(ctx, org.ID)
	if err != nil {
		return err
	}
	owners := 0
	for _, m := range members {
		if m.Role == meta.OrgRoleOwner {
			owners++
		}
	}
	got, err := s.Store.GetOrgMember(ctx, org.ID, login)
	if err != nil {
		return err
	}
	if got.Role == meta.OrgRoleOwner && owners <= 1 {
		return fmt.Errorf("forbidden: cannot remove the last owner")
	}
	return s.Store.RemoveOrgMember(ctx, org.ID, login)
}

func (s *Service) DeleteOrg(ctx context.Context, nameOrID string) error {
	org, err := s.requireOrgOwnerNamed(ctx, nameOrID)
	if err != nil {
		return err
	}
	if org.Name == meta.DefaultOrg && org.CreatedBy == auth.OwnerFrom(ctx) {
		return fmt.Errorf("forbidden: cannot delete your default org")
	}
	cons, err := s.Store.ListConnectors(ctx)
	if err != nil {
		return err
	}
	for _, c := range cons {
		if c.OrgID == org.ID {
			return fmt.Errorf("org_has_connectors: delete connectors first")
		}
	}
	return s.Store.DeleteOrg(ctx, org.ID)
}

func (s *Service) requireOrgMemberNamed(ctx context.Context, nameOrID string) (meta.Org, error) {
	if nameOrID == "" {
		nameOrID = auth.OrgNameFrom(ctx)
	}
	org, err := s.ResolveOrg(ctx, nameOrID)
	if err != nil {
		return meta.Org{}, err
	}
	if auth.OwnerFrom(ctx) == "" {
		return org, nil
	}
	if _, err := s.Store.GetOrgMember(ctx, org.ID, auth.OwnerFrom(ctx)); err != nil {
		return meta.Org{}, fmt.Errorf("forbidden: not a member of org %q", org.Name)
	}
	return org, nil
}

func (s *Service) requireOrgOwnerNamed(ctx context.Context, nameOrID string) (meta.Org, error) {
	org, err := s.requireOrgMemberNamed(ctx, nameOrID)
	if err != nil {
		return meta.Org{}, err
	}
	if auth.OwnerFrom(ctx) == "" {
		return org, nil
	}
	m, err := s.Store.GetOrgMember(ctx, org.ID, auth.OwnerFrom(ctx))
	if err != nil {
		return meta.Org{}, err
	}
	if m.Role != meta.OrgRoleOwner {
		return meta.Org{}, fmt.Errorf("forbidden: org owner required")
	}
	return org, nil
}

func (s *Service) currentOrgID(ctx context.Context) string {
	return auth.OrgIDFrom(ctx)
}

func (s *Service) orgRole(ctx context.Context) string {
	id := auth.OrgIDFrom(ctx)
	login := auth.OwnerFrom(ctx)
	if id == "" || login == "" {
		return meta.OrgRoleOwner
	}
	m, err := s.Store.GetOrgMember(ctx, id, login)
	if err != nil {
		return ""
	}
	return m.Role
}

func (s *Service) requireOrgOwner(ctx context.Context) error {
	if auth.OwnerFrom(ctx) == "" {
		return nil
	}
	if s.orgRole(ctx) != meta.OrgRoleOwner {
		return fmt.Errorf("forbidden: org owner required")
	}
	return nil
}

func (s *Service) canMutateBranch(ctx context.Context, rec meta.BranchRecord) error {
	login := auth.OwnerFrom(ctx)
	if login == "" {
		return nil
	}
	if rec.CreatedBy == login {
		return nil
	}
	if s.orgRole(ctx) == meta.OrgRoleOwner {
		return nil
	}
	return fmt.Errorf("forbidden: only the branch creator or an org owner can do that")
}

func (s *Service) filterConnectorsForActor(ctx context.Context, list []meta.Connector) []meta.Connector {
	orgID := auth.OrgIDFrom(ctx)
	owner := auth.OwnerFrom(ctx)
	if orgID != "" {
		var out []meta.Connector
		for _, c := range list {
			if c.OrgID == orgID || (c.OrgID == "" && c.CreatedBy == owner) {
				out = append(out, c)
			}
		}
		return out
	}
	return meta.FilterConnectorsByOwner(owner, list)
}

func (s *Service) filterBranchesForActor(ctx context.Context, list []meta.BranchRecord) []meta.BranchRecord {
	orgID := auth.OrgIDFrom(ctx)
	owner := auth.OwnerFrom(ctx)
	if orgID != "" {
		var out []meta.BranchRecord
		for _, b := range list {
			if b.OrgID == orgID || (b.OrgID == "" && b.CreatedBy == owner) {
				out = append(out, b)
			}
		}
		return out
	}
	return meta.FilterBranchesByOwner(owner, list)
}
