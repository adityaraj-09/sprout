package mongoproxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/adityaraj/sprout/internal/meta"
	"github.com/adityaraj/sprout/internal/pgproxy"
	"github.com/adityaraj/sprout/internal/postgres"
)

func TestStoreResolverMongoOnly(t *testing.T) {
	t.Setenv("SPROUT_PUBLIC_HOST", "strido.fit")
	t.Setenv("SPROUT_BRANCH_SUBDOMAIN", "true")
	ctx := context.Background()
	store, err := meta.OpenFile(filepath.Join(t.TempDir(), "control.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	proj, err := store.EnsureProject(ctx, "default")
	if err != nil {
		t.Fatal(err)
	}
	_ = store.PutConnector(ctx, meta.Connector{
		ID: "pg", ProjectID: proj.ID, Name: "supabase", Engine: "postgres",
		Status: meta.ConnectorReplicating, Port: 55434,
	})
	_ = store.PutConnector(ctx, meta.Connector{
		ID: "mo", ProjectID: proj.ID, Name: "atlas", Engine: "mongodb",
		Status: meta.ConnectorReplicating, Port: 55461,
	})
	_ = store.PutBranch(ctx, meta.BranchRecord{
		ID: "b1", ProjectID: proj.ID, Name: "feat", Role: "branch",
		Status: meta.StatusActive, Port: 55462, SourceConnector: "atlas", SourceConnectorID: "mo",
	})
	_ = store.PutBranch(ctx, meta.BranchRecord{
		ID: "b2", ProjectID: proj.ID, Name: "feat", Role: "branch",
		Status: meta.StatusActive, Port: 55440, SourceConnector: "supabase", SourceConnectorID: "pg",
	})
	resolve := StoreResolver(store)
	_, port, err := resolve("atlas.strido.fit")
	if err != nil || port != 55461 {
		t.Fatalf("atlas connector: port=%d err=%v", port, err)
	}
	_, port, err = resolve("feat-atlas.strido.fit")
	if err != nil || port != 55462 {
		t.Fatalf("mongo branch: port=%d err=%v", port, err)
	}
	if _, _, err := resolve("supabase.strido.fit"); err == nil {
		t.Fatal("postgres connector must not route on mongo proxy")
	}
	if _, _, err := resolve("feat-supabase.strido.fit"); err == nil {
		t.Fatal("postgres branch must not route on mongo proxy")
	}
}

func TestProxyPassthroughSNI(t *testing.T) {
	t.Setenv("SPROUT_PUBLIC_HOST", "strido.fit")
	backend := tlsEchoServer(t)
	_, backendPort, err := net.SplitHostPort(backend.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		Addr: "127.0.0.1:0",
		Resolve: func(sni string) (string, int, error) {
			if postgres.NormalizeSNI(sni) != "feat-atlas.strido.fit" {
				return "", 0, fmt.Errorf("unexpected sni %q", sni)
			}
			var p int
			_, _ = fmt.Sscanf(backendPort, "%d", &p)
			return "127.0.0.1", p, nil
		},
	}
	if err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	go func() { _ = s.Serve() }()

	tlsConn := tls.Client(mustDial(t, s.BoundAddr()), &tls.Config{
		InsecureSkipVerify: true,
		ServerName:         "feat-atlas.strido.fit",
	})
	defer tlsConn.Close()
	if err := tlsConn.Handshake(); err != nil {
		t.Fatal(err)
	}
	if _, err := tlsConn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	_ = tlsConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(tlsConn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "hello" {
		t.Fatalf("got %q", buf)
	}
}

func mustDial(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func tlsEchoServer(t *testing.T) net.Listener {
	t.Helper()
	tlsCfg, err := pgproxy.LoadTLSConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", tlsCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()
	return ln
}
