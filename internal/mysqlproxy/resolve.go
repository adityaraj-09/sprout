package mysqlproxy

import (
	"context"
	"fmt"
	"time"

	"github.com/adityaraj/sprout/internal/engine"
	"github.com/adityaraj/sprout/internal/meta"
	"github.com/adityaraj/sprout/internal/mysql"
	"github.com/adityaraj/sprout/internal/postgres"
)

// Target is the local mysqld the proxy logs into after SNI + client auth.
type Target struct {
	Host     string
	Port     int
	Password string
}

// Resolver maps a TLS server name to a MySQL instance.
type Resolver func(sni string) (Target, error)

// StoreResolver looks up MySQL connectors and their branches by advertised hostname.
// Postgres rows are ignored so the same label space can be shared with pgproxy.
func StoreResolver(store meta.Store) Resolver {
	return func(sni string) (Target, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		sni = postgres.NormalizeSNI(sni)
		if sni == "" {
			return Target{}, fmt.Errorf("missing TLS server name")
		}
		backend := mysql.ProxyBackendHost()
		conns, err := store.ListConnectors(ctx)
		if err != nil {
			return Target{}, err
		}
		mysqlIDs, mysqlKeys := mysqlConnectorIndex(conns)
		branches, err := store.ListAllBranches(ctx)
		if err != nil {
			return Target{}, err
		}
		for _, b := range branches {
			if b.Port <= 0 || b.Role == "replica" {
				continue
			}
			if !branchFromMySQL(b, mysqlIDs, mysqlKeys) {
				continue
			}
			from := ""
			if b.Role == "branch" {
				from = b.SourceConnector
			}
			if postgres.MatchesSNI(sni, b.Name, from, b.CreatedBy) {
				return Target{Host: backend, Port: b.Port, Password: b.Password}, nil
			}
		}
		for _, c := range conns {
			if c.Port <= 0 || !engine.IsMySQL(c.Engine) {
				continue
			}
			if postgres.MatchesSNI(sni, c.Name, "", c.CreatedBy) {
				return Target{Host: backend, Port: c.Port, Password: c.Password}, nil
			}
		}
		return Target{}, fmt.Errorf("unknown server name %q", sni)
	}
}

func mysqlConnectorIndex(conns []meta.Connector) (ids map[string]bool, keys map[string]bool) {
	ids = make(map[string]bool)
	keys = make(map[string]bool)
	for _, c := range conns {
		if !engine.IsMySQL(c.Engine) {
			continue
		}
		ids[c.ID] = true
		keys[c.Name+"\x00"+c.CreatedBy] = true
	}
	return ids, keys
}

func branchFromMySQL(b meta.BranchRecord, ids, keys map[string]bool) bool {
	if b.SourceConnectorID != "" && ids[b.SourceConnectorID] {
		return true
	}
	if b.SourceConnector != "" && keys[b.SourceConnector+"\x00"+b.CreatedBy] {
		return true
	}
	return false
}
