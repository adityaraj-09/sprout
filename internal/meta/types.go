package meta

import (
	"context"
	"time"
)

// Branch statuses — Phase 2 state machine.
const (
	StatusCreating  = "creating"
	StatusActive    = "active"
	StatusIdle      = "idle"
	StatusCrashed   = "crashed" // compute died; not a user suspend
	StatusResetting = "resetting"
	StatusDeleting  = "deleting"
	StatusError     = "error"
)

// Connector statuses — Phase 3.
const (
	ConnectorBootstrapping = "bootstrapping"
	ConnectorReplicating   = "replicating"
	ConnectorIdle          = "idle" // compute stopped; data kept (suspend)
	ConnectorCrashed       = "crashed"
	ConnectorError         = "error"
	ConnectorDisconnected  = "disconnected"
)

type BranchRecord struct {
	ID                string    `json:"id"`
	ProjectID         string    `json:"project_id"`
	Name              string    `json:"name"`
	Role              string    `json:"role"` // main | replica | branch
	Status            string    `json:"status"`
	Port              int       `json:"port"`
	DataDir           string    `json:"data_dir"`
	SnapshotRef       string    `json:"snapshot_ref"`
	ContainerID       string    `json:"container_id,omitempty"`
	Compute           string    `json:"compute,omitempty"`
	ConnString        string    `json:"connection_string"`
	ErrorMessage      string    `json:"error_message,omitempty"`
	SourceLSN         string    `json:"source_lsn,omitempty"`
	SourceConnector   string    `json:"source_connector,omitempty"` // connector name used as parent
	SourceConnectorID string    `json:"source_connector_id,omitempty"`
	Password          string    `json:"-"`
	CreatedBy         string    `json:"created_by,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	LastUsedAt        time.Time `json:"last_used_at"`
}

type Project struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// Connector is a named upstream link with its own local replica dataset.
type Connector struct {
	ID           string    `json:"id"`
	ProjectID    string    `json:"project_id"`
	Name         string    `json:"name"` // unique per project+owner (e.g. supabase)
	PrimaryURL   string    `json:"primary_url"`
	Engine       string    `json:"engine,omitempty"` // postgres (default) | mysql
	Mode         string    `json:"mode"`             // physical | logical
	Status       string    `json:"status"`
	DataDir      string    `json:"data_dir"`
	Port         int       `json:"port"`
	ErrorMessage string    `json:"error_message,omitempty"`
	LastLSN      string    `json:"last_lsn,omitempty"`
	LastLagBytes int64     `json:"last_lag_bytes"`
	Password     string    `json:"-"`
	CreatedBy    string    `json:"created_by,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Store is the control-plane notebook.
type Store interface {
	EnsureProject(ctx context.Context, name string) (Project, error)
	GetProject(ctx context.Context, idOrName string) (Project, error)
	ListProjects(ctx context.Context) ([]Project, error)

	AllocPort(ctx context.Context) (int, error)

	PutBranch(ctx context.Context, b BranchRecord) error
	GetBranch(ctx context.Context, projectID, name string) (BranchRecord, error)
	FindBranch(ctx context.Context, projectID, name, from, owner string) (BranchRecord, error)
	GetBranchByID(ctx context.Context, id string) (BranchRecord, error)
	ListBranches(ctx context.Context, projectID string) ([]BranchRecord, error)
	ListAllBranches(ctx context.Context) ([]BranchRecord, error)
	DeleteBranch(ctx context.Context, id string) error
	UpdateBranch(ctx context.Context, b BranchRecord) error

	PutConnector(ctx context.Context, c Connector) error
	GetConnectorByID(ctx context.Context, id string) (Connector, error)
	GetConnectorByName(ctx context.Context, projectID, name, owner string) (Connector, error)
	ListConnectors(ctx context.Context) ([]Connector, error)
	ListConnectorsByProject(ctx context.Context, projectID string) ([]Connector, error)
	UpdateConnector(ctx context.Context, c Connector) error
	DeleteConnector(ctx context.Context, id string) error

	Close() error
}
