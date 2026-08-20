package meta

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

func (s *FileStore) EnsureDefaultOrg(ctx context.Context, login string) (Org, error) {
	_ = ctx
	login = strings.ToLower(strings.TrimSpace(login))
	if login == "" {
		return Org{}, fmt.Errorf("invalid_name: org owner required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ensureOrgLocked(login, DefaultOrg)
}

func (s *FileStore) ensureOrgLocked(login, name string) (Org, error) {
	login = strings.ToLower(strings.TrimSpace(login))
	name = strings.ToLower(strings.TrimSpace(name))
	if s.data.Orgs == nil {
		s.data.Orgs = map[string]Org{}
	}
	for _, o := range s.data.Orgs {
		if o.CreatedBy == login && o.Name == name {
			o.Role = OrgRoleOwner
			_ = s.addMemberLocked(OrgMember{OrgID: o.ID, Login: login, Role: OrgRoleOwner, AddedBy: login})
			return o, s.saveLocked()
		}
	}
	o := Org{ID: uuid.NewString(), Name: name, CreatedBy: login, CreatedAt: time.Now().UTC(), Role: OrgRoleOwner}
	s.data.Orgs[o.ID] = o
	_ = s.addMemberLocked(OrgMember{OrgID: o.ID, Login: login, Role: OrgRoleOwner, AddedBy: login})
	return o, s.saveLocked()
}

func (s *FileStore) CreateOrg(ctx context.Context, login, name string) (Org, error) {
	_ = ctx
	login = strings.ToLower(strings.TrimSpace(login))
	name = strings.ToLower(strings.TrimSpace(name))
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.data.Orgs {
		if o.CreatedBy == login && o.Name == name {
			return Org{}, fmt.Errorf("org_exists: %q", name)
		}
	}
	return s.ensureOrgLocked(login, name)
}

func (s *FileStore) GetOrg(ctx context.Context, id string) (Org, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.data.Orgs[id]
	if !ok {
		return Org{}, fmt.Errorf("org_not_found")
	}
	return o, nil
}

func (s *FileStore) ListOrgs(ctx context.Context, login string) ([]Org, error) {
	_ = ctx
	login = strings.ToLower(strings.TrimSpace(login))
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Org
	for _, o := range s.data.Orgs {
		if login == "" {
			out = append(out, o)
			continue
		}
		for _, m := range s.data.OrgMembers {
			if m.OrgID == o.ID && m.Login == login {
				o.Role = m.Role
				out = append(out, o)
				break
			}
		}
	}
	return out, nil
}

func (s *FileStore) DeleteOrg(ctx context.Context, id string) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Orgs[id]; !ok {
		return fmt.Errorf("org_not_found")
	}
	delete(s.data.Orgs, id)
	kept := s.data.OrgMembers[:0]
	for _, m := range s.data.OrgMembers {
		if m.OrgID != id {
			kept = append(kept, m)
		}
	}
	s.data.OrgMembers = kept
	return s.saveLocked()
}

func (s *FileStore) addMemberLocked(m OrgMember) error {
	m.Login = strings.ToLower(strings.TrimSpace(m.Login))
	m.AddedBy = strings.ToLower(strings.TrimSpace(m.AddedBy))
	if m.Login == "" || m.OrgID == "" {
		return fmt.Errorf("invalid_name: member login required")
	}
	if m.Role != OrgRoleOwner && m.Role != OrgRoleMember {
		m.Role = OrgRoleMember
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	for i, existing := range s.data.OrgMembers {
		if existing.OrgID == m.OrgID && existing.Login == m.Login {
			s.data.OrgMembers[i] = m
			return nil
		}
	}
	s.data.OrgMembers = append(s.data.OrgMembers, m)
	return nil
}

func (s *FileStore) AddOrgMember(ctx context.Context, m OrgMember) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.addMemberLocked(m); err != nil {
		return err
	}
	return s.saveLocked()
}

func (s *FileStore) RemoveOrgMember(ctx context.Context, orgID, login string) error {
	_ = ctx
	login = strings.ToLower(strings.TrimSpace(login))
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.data.OrgMembers[:0]
	found := false
	for _, m := range s.data.OrgMembers {
		if m.OrgID == orgID && m.Login == login {
			found = true
			continue
		}
		kept = append(kept, m)
	}
	if !found {
		return fmt.Errorf("member_not_found")
	}
	s.data.OrgMembers = kept
	return s.saveLocked()
}

func (s *FileStore) ListOrgMembers(ctx context.Context, orgID string) ([]OrgMember, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []OrgMember
	for _, m := range s.data.OrgMembers {
		if m.OrgID == orgID {
			out = append(out, m)
		}
	}
	return out, nil
}

func (s *FileStore) GetOrgMember(ctx context.Context, orgID, login string) (OrgMember, error) {
	_ = ctx
	login = strings.ToLower(strings.TrimSpace(login))
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.data.OrgMembers {
		if m.OrgID == orgID && m.Login == login {
			return m, nil
		}
	}
	return OrgMember{}, fmt.Errorf("member_not_found")
}

func (s *FileStore) GetConnectorByNameInOrg(ctx context.Context, projectID, name, orgID string) (Connector, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.data.Connectors {
		if c.ProjectID == projectID && c.Name == name && c.OrgID == orgID {
			return c, nil
		}
	}
	return Connector{}, fmt.Errorf("connector not found: %s", name)
}
