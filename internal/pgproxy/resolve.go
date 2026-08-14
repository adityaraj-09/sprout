package pgproxy

import (
	"context"
	"fmt"
	"time"

	"github.com/adityaraj/sprout/internal/meta"
	"github.com/adityaraj/sprout/internal/postgres"
)

// Resolver maps a TLS server name to a backend host:port.
type Resolver func(sni string) (host string, port int, err error)

// StoreResolver looks up connectors and branches by advertised hostname.
func StoreResolver(store meta.Store) Resolver {
	return func(sni string) (string, int, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		sni = postgres.NormalizeSNI(sni)
		if sni == "" {
			return "", 0, fmt.Errorf("missing TLS server name")
		}
		backend := postgres.ProxyBackendHost()
		branches, err := store.ListAllBranches(ctx)
		if err != nil {
			return "", 0, err
		}
		for _, b := range branches {
			if b.Port <= 0 || b.Role == "replica" {
				continue
			}
			from := ""
			if b.Role == "branch" {
				from = b.SourceConnector
			}
			if postgres.MatchesSNI(sni, b.Name, from, b.CreatedBy) {
				return backend, b.Port, nil
			}
		}
		conns, err := store.ListConnectors(ctx)
		if err != nil {
			return "", 0, err
		}
		for _, c := range conns {
			if c.Port <= 0 {
				continue
			}
			if postgres.MatchesSNI(sni, c.Name, "", c.CreatedBy) {
				return backend, c.Port, nil
			}
		}
		return "", 0, fmt.Errorf("unknown server name %q", sni)
	}
}
