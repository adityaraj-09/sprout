package qdrantproxy

import (
	"context"
	"fmt"
	"time"

	"github.com/adityaraj/sprout/internal/engine"
	"github.com/adityaraj/sprout/internal/meta"
	"github.com/adityaraj/sprout/internal/pgproxy"
	"github.com/adityaraj/sprout/internal/postgres"
	"github.com/adityaraj/sprout/internal/qdrant"
)

// StoreResolver maps TLS server names to Qdrant connectors/branches only.
func StoreResolver(store meta.Store) pgproxy.Resolver {
	return func(sni string) (string, int, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		sni = postgres.NormalizeSNI(sni)
		if sni == "" {
			return "", 0, fmt.Errorf("missing TLS server name")
		}
		backend := qdrant.ProxyBackendHost()

		ids := map[string]struct{}{}
		conns, err := store.ListConnectors(ctx)
		if err != nil {
			return "", 0, err
		}
		for _, c := range conns {
			if c.Port <= 0 || !engine.IsQdrant(c.Engine) {
				continue
			}
			ids[c.ID] = struct{}{}
			if postgres.MatchesSNI(sni, c.Name, "", c.CreatedBy) {
				return backend, c.Port, nil
			}
		}

		branches, err := store.ListAllBranches(ctx)
		if err != nil {
			return "", 0, err
		}
		for _, b := range branches {
			if b.Port <= 0 || b.Role == "replica" || b.Role == "main" {
				continue
			}
			if b.SourceConnectorID != "" {
				if _, ok := ids[b.SourceConnectorID]; !ok {
					continue
				}
			} else {
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
		return "", 0, fmt.Errorf("unknown qdrant server name %q", sni)
	}
}
