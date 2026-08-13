package meta

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
)

// FileStore is a crash-safer control-plane notebook (JSON + mutex + atomic rename).
type FileStore struct {
	path string
	mu   sync.Mutex
	data fileData
}

type fileData struct {
	NextPort   int                     `json:"next_port"`
	Projects   map[string]Project      `json:"projects"`
	Branches   map[string]BranchRecord `json:"branches"`   // keyed by id
	Connectors map[string]Connector    `json:"connectors"` // keyed by connector id
}

func OpenFile(path string) (*FileStore, error) {
	s := &FileStore{path: path}
	s.data.Projects = map[string]Project{}
	s.data.Branches = map[string]BranchRecord{}
	s.data.Connectors = map[string]Connector{}
	s.data.NextPort = 55433
	b, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(b, &s.data); err != nil {
			return nil, err
		}
		if s.data.Projects == nil {
			s.data.Projects = map[string]Project{}
		}
		if s.data.Branches == nil {
			s.data.Branches = map[string]BranchRecord{}
		}
		if s.data.Connectors == nil {
			s.data.Connectors = map[string]Connector{}
		}
		if s.data.NextPort == 0 {
			s.data.NextPort = 55433
		}
		s.migrateConnectorsLocked()
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}

// migrateConnectorsLocked upgrades legacy project_id-keyed connectors to id-keyed.
func (s *FileStore) migrateConnectorsLocked() {
	migrated := map[string]Connector{}
	changed := false
	for key, c := range s.data.Connectors {
		if c.ID == "" {
			c.ID = uuid.NewString()
			changed = true
		}
		if c.Name == "" {
			c.Name = "primary"
			changed = true
		}
		if c.Port == 0 {
			// Legacy singleton used fixed main port
			c.Port = 55432
			changed = true
		}
		if key != c.ID {
			changed = true
		}
		migrated[c.ID] = c
	}
	if changed {
		s.data.Connectors = migrated
		_ = s.saveLocked()
	}
}

func (s *FileStore) Close() error { return nil }

func (s *FileStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *FileStore) EnsureProject(ctx context.Context, name string) (Project, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.data.Projects {
		if p.Name == name {
			return p, nil
		}
	}
	p := Project{ID: uuid.NewString(), Name: name, CreatedAt: time.Now().UTC()}
	s.data.Projects[p.ID] = p
	return p, s.saveLocked()
}

func (s *FileStore) GetProject(ctx context.Context, idOrName string) (Project, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.data.Projects[idOrName]; ok {
		return p, nil
	}
	for _, p := range s.data.Projects {
		if p.Name == idOrName {
			return p, nil
		}
	}
	return Project{}, fmt.Errorf("project not found: %s", idOrName)
}

func (s *FileStore) ListProjects(ctx context.Context) ([]Project, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Project, 0, len(s.data.Projects))
	for _, p := range s.data.Projects {
		out = append(out, p)
	}
	return out, nil
}

func (s *FileStore) AllocPort(ctx context.Context) (int, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.data.NextPort
	s.data.NextPort++
	return p, s.saveLocked()
}

func (s *FileStore) PutBranch(ctx context.Context, b BranchRecord) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	if b.ID == "" {
		b.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	if b.CreatedAt.IsZero() {
		b.CreatedAt = now
	}
	b.UpdatedAt = now
	if b.LastUsedAt.IsZero() {
		b.LastUsedAt = now
	}
	for _, existing := range s.data.Branches {
		if existing.ID == b.ID {
			continue
		}
		if existing.ProjectID == b.ProjectID && existing.Name == b.Name && existing.SourceConnector == b.SourceConnector {
			return fmt.Errorf("branch_exists: %q from %s", b.Name, b.SourceConnector)
		}
	}
	s.data.Branches[b.ID] = b
	return s.saveLocked()
}

func (s *FileStore) UpdateBranch(ctx context.Context, b BranchRecord) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Branches[b.ID]; !ok {
		return fmt.Errorf("branch not found: %s", b.ID)
	}
	b.UpdatedAt = time.Now().UTC()
	s.data.Branches[b.ID] = b
	return s.saveLocked()
}

func (s *FileStore) GetBranch(ctx context.Context, projectID, name string) (BranchRecord, error) {
	return s.FindBranch(ctx, projectID, name, "")
}

func (s *FileStore) FindBranch(ctx context.Context, projectID, name, from string) (BranchRecord, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	var matches []BranchRecord
	for _, b := range s.data.Branches {
		if b.ProjectID == projectID && b.Name == name {
			matches = append(matches, b)
		}
	}
	return ResolveBranch(name, from, matches)
}

func (s *FileStore) GetBranchByID(ctx context.Context, id string) (BranchRecord, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.data.Branches[id]
	if !ok {
		return BranchRecord{}, fmt.Errorf("branch not found")
	}
	return b, nil
}

func (s *FileStore) ListBranches(ctx context.Context, projectID string) ([]BranchRecord, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []BranchRecord
	for _, b := range s.data.Branches {
		if b.ProjectID == projectID {
			out = append(out, b)
		}
	}
	return out, nil
}

func (s *FileStore) ListAllBranches(ctx context.Context) ([]BranchRecord, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]BranchRecord, 0, len(s.data.Branches))
	for _, b := range s.data.Branches {
		out = append(out, b)
	}
	return out, nil
}

func (s *FileStore) DeleteBranch(ctx context.Context, id string) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data.Branches, id)
	return s.saveLocked()
}

func (s *FileStore) PutConnector(ctx context.Context, c Connector) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.ID == "" {
		c.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	if s.data.Connectors == nil {
		s.data.Connectors = map[string]Connector{}
	}
	// Enforce unique (project_id, name)
	for id, existing := range s.data.Connectors {
		if existing.ProjectID == c.ProjectID && existing.Name == c.Name && id != c.ID {
			return fmt.Errorf("connector_exists: %q", c.Name)
		}
	}
	s.data.Connectors[c.ID] = c
	return s.saveLocked()
}

func (s *FileStore) GetConnectorByID(ctx context.Context, id string) (Connector, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.data.Connectors[id]
	if !ok {
		return Connector{}, fmt.Errorf("connector not found")
	}
	return c, nil
}

func (s *FileStore) GetConnectorByName(ctx context.Context, projectID, name string) (Connector, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.data.Connectors {
		if c.ProjectID == projectID && c.Name == name {
			return c, nil
		}
	}
	return Connector{}, fmt.Errorf("connector not found: %s", name)
}

func (s *FileStore) ListConnectors(ctx context.Context) ([]Connector, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Connector, 0, len(s.data.Connectors))
	for _, c := range s.data.Connectors {
		out = append(out, c)
	}
	return out, nil
}

func (s *FileStore) ListConnectorsByProject(ctx context.Context, projectID string) ([]Connector, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Connector
	for _, c := range s.data.Connectors {
		if c.ProjectID == projectID {
			out = append(out, c)
		}
	}
	return out, nil
}

func (s *FileStore) UpdateConnector(ctx context.Context, c Connector) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Connectors[c.ID]; !ok {
		return fmt.Errorf("connector not found")
	}
	c.UpdatedAt = time.Now().UTC()
	s.data.Connectors[c.ID] = c
	return s.saveLocked()
}

func (s *FileStore) DeleteConnector(ctx context.Context, id string) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Connectors[id]; ok {
		delete(s.data.Connectors, id)
		return s.saveLocked()
	}
	for cid, c := range s.data.Connectors {
		if c.Name == id {
			delete(s.data.Connectors, cid)
			return s.saveLocked()
		}
	}
	return fmt.Errorf("connector not found")
}
