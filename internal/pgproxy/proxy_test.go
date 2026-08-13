package pgproxy

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/adityaraj/sprout/internal/meta"
	"github.com/adityaraj/sprout/internal/postgres"
)

func TestStoreResolverSNI(t *testing.T) {
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
		ID: "cx", ProjectID: proj.ID, Name: "x", Status: meta.ConnectorReplicating, Port: 55434,
	})
	_ = store.PutConnector(ctx, meta.Connector{
		ID: "cy", ProjectID: proj.ID, Name: "y", Status: meta.ConnectorReplicating, Port: 55435,
	})
	_ = store.PutBranch(ctx, meta.BranchRecord{
		ID: "b1", ProjectID: proj.ID, Name: "test", Role: "branch",
		Status: meta.StatusActive, Port: 55440, SourceConnector: "x",
	})
	_ = store.PutBranch(ctx, meta.BranchRecord{
		ID: "b2", ProjectID: proj.ID, Name: "test", Role: "branch",
		Status: meta.StatusActive, Port: 55441, SourceConnector: "y",
	})
	resolve := StoreResolver(store)
	host, port, err := resolve("test-x.strido.fit")
	if err != nil || port != 55440 {
		t.Fatalf("test-x: host=%s port=%d err=%v", host, port, err)
	}
	_, port, err = resolve("test-y.strido.fit")
	if err != nil || port != 55441 {
		t.Fatalf("test-y: port=%d err=%v", port, err)
	}
	_, port, err = resolve("x.strido.fit")
	if err != nil || port != 55434 {
		t.Fatalf("connector x: port=%d err=%v", port, err)
	}
	if _, _, err := resolve("missing.strido.fit"); err == nil {
		t.Fatal("expected unknown server name")
	}
}

func TestProxySNIRoutes(t *testing.T) {
	backend := echoServer(t)
	_, backendPort, err := net.SplitHostPort(backend.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	tlsCfg, err := LoadTLSConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		Addr:      "127.0.0.1:0",
		TLSConfig: tlsCfg,
		Resolve: func(sni string) (string, int, error) {
			if postgres.NormalizeSNI(sni) != "test-x.strido.fit" {
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
	addr := s.BoundAddr()

	raw, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var sslReq [8]byte
	binary.BigEndian.PutUint32(sslReq[0:4], 8)
	binary.BigEndian.PutUint32(sslReq[4:8], sslRequestCode)
	if _, err := raw.Write(sslReq[:]); err != nil {
		t.Fatal(err)
	}
	var reply [1]byte
	if _, err := io.ReadFull(raw, reply[:]); err != nil || reply[0] != 'S' {
		t.Fatalf("ssl reply %q %v", reply, err)
	}
	tlsConn := tls.Client(raw, &tls.Config{
		InsecureSkipVerify: true,
		ServerName:         "test-x.strido.fit",
	})
	if err := tlsConn.Handshake(); err != nil {
		t.Fatal(err)
	}
	if _, err := tlsConn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if err := tlsConn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(tlsConn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "hello" {
		t.Fatalf("got %q", buf)
	}
}

func echoServer(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
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
