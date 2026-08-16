package mysqlproxy

import (
	"bytes"
	"crypto/tls"
	"encoding/binary"
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

func TestStoreResolverMySQLOnly(t *testing.T) {
	t.Setenv("SPROUT_PUBLIC_HOST", "strido.fit")
	t.Setenv("SPROUT_BRANCH_SUBDOMAIN", "true")
	ctx := t.Context()
	store, err := meta.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	proj, err := store.EnsureProject(ctx, "default")
	if err != nil {
		t.Fatal(err)
	}
	_ = store.PutConnector(ctx, meta.Connector{
		ID: "cx", ProjectID: proj.ID, Name: "lab", Engine: "postgres",
		Status: meta.ConnectorReplicating, Port: 55434, Password: "pg-secret",
	})
	_ = store.PutConnector(ctx, meta.Connector{
		ID: "cm", ProjectID: proj.ID, Name: "shop", Engine: "mysql",
		Status: meta.ConnectorReplicating, Port: 33061, Password: "mysql-conn",
	})
	_ = store.PutBranch(ctx, meta.BranchRecord{
		ID: "b1", ProjectID: proj.ID, Name: "test", Role: "branch",
		Status: meta.StatusActive, Port: 55440, SourceConnector: "lab", SourceConnectorID: "cx",
		Password: "pg-branch",
	})
	_ = store.PutBranch(ctx, meta.BranchRecord{
		ID: "bm", ProjectID: proj.ID, Name: "feat", Role: "branch",
		Status: meta.StatusActive, Port: 33062, SourceConnector: "shop", SourceConnectorID: "cm",
		Password: "mysql-branch",
	})
	resolve := StoreResolver(store)
	got, err := resolve("shop.strido.fit")
	if err != nil || got.Port != 33061 || got.Password != "mysql-conn" {
		t.Fatalf("connector: %+v %v", got, err)
	}
	got, err = resolve("feat-shop.strido.fit")
	if err != nil || got.Port != 33062 || got.Password != "mysql-branch" {
		t.Fatalf("branch: %+v %v", got, err)
	}
	if _, err := resolve("lab.strido.fit"); err == nil {
		t.Fatal("postgres connector must not resolve on mysqlproxy")
	}
	if _, err := resolve("test-lab.strido.fit"); err == nil {
		t.Fatal("postgres branch must not resolve on mysqlproxy")
	}
	if _, err := resolve("missing.strido.fit"); err == nil {
		t.Fatal("expected unknown server name")
	}
}

func TestRejectsCleartextSSL(t *testing.T) {
	tlsCfg, err := pgproxy.LoadTLSConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		Addr:      "127.0.0.1:0",
		TLSConfig: tlsCfg,
		Resolve:   func(string) (Target, error) { return Target{}, fmt.Errorf("unused") },
	}
	if err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	go func() { _ = s.Serve() }()

	raw, err := net.DialTimeout("tcp", s.BoundAddr(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := readPacket(raw); err != nil {
		t.Fatal(err)
	}
	plain := make([]byte, 32)
	binary.LittleEndian.PutUint32(plain[0:4], clientProtocol41|clientSecureConnection)
	binary.LittleEndian.PutUint32(plain[4:8], 16<<20)
	plain[8] = 0x21
	if err := writePacket(raw, 1, plain); err != nil {
		t.Fatal(err)
	}
	_, payload, err := readPacket(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) == 0 || payload[0] != 0xff {
		t.Fatalf("expected ERR, got %x", payload)
	}
	if !bytes.Contains(payload, []byte("--ssl-mode=REQUIRED")) {
		t.Fatalf("hint missing: %q", payload)
	}
}

func TestProxySNIAuthAndSplice(t *testing.T) {
	const pass = "s3cret"
	backend := fakeMySQLBackend(t, pass)
	_, backendPort, err := net.SplitHostPort(backend.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	var port int
	_, _ = fmt.Sscanf(backendPort, "%d", &port)

	tlsCfg, err := pgproxy.LoadTLSConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		Addr:      "127.0.0.1:0",
		TLSConfig: tlsCfg,
		Resolve: func(sni string) (Target, error) {
			if postgres.NormalizeSNI(sni) != "shop.strido.fit" {
				return Target{}, fmt.Errorf("unexpected sni %q", sni)
			}
			return Target{Host: "127.0.0.1", Port: port, Password: pass}, nil
		},
	}
	if err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	go func() { _ = s.Serve() }()

	tlsConn, salt := mysqlTLSClient(t, s.BoundAddr(), "shop.strido.fit")
	defer tlsConn.Close()
	resp := buildHandshakeResponse("sprout", "", nativePassword(pass, salt), nativePlugin)
	if err := writePacket(tlsConn, 2, resp); err != nil {
		t.Fatal(err)
	}
	_, payload, err := readPacket(tlsConn)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) == 0 || payload[0] != 0x00 {
		t.Fatalf("expected OK, got %x", payload)
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

func TestProxyWrongPassword(t *testing.T) {
	backend := fakeMySQLBackend(t, "right")
	_, backendPort, _ := net.SplitHostPort(backend.Addr().String())
	var port int
	_, _ = fmt.Sscanf(backendPort, "%d", &port)
	tlsCfg, err := pgproxy.LoadTLSConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		Addr:      "127.0.0.1:0",
		TLSConfig: tlsCfg,
		Resolve:   func(string) (Target, error) { return Target{Host: "127.0.0.1", Port: port, Password: "right"}, nil },
	}
	if err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	go func() { _ = s.Serve() }()

	tlsConn, salt := mysqlTLSClient(t, s.BoundAddr(), "shop.strido.fit")
	defer tlsConn.Close()
	resp := buildHandshakeResponse("sprout", "", nativePassword("wrong", salt), nativePlugin)
	if err := writePacket(tlsConn, 2, resp); err != nil {
		t.Fatal(err)
	}
	_, payload, err := readPacket(tlsConn)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) == 0 || payload[0] != 0xff {
		t.Fatalf("expected access denied, got %x", payload)
	}
}

func mysqlTLSClient(t *testing.T, addr, sni string) (*tls.Conn, []byte) {
	t.Helper()
	raw, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = raw.SetDeadline(time.Now().Add(5 * time.Second))
	_, greeting, err := readPacket(raw)
	if err != nil {
		t.Fatal(err)
	}
	g, err := parseGreeting(greeting)
	if err != nil {
		t.Fatal(err)
	}
	sslReq := make([]byte, 32)
	binary.LittleEndian.PutUint32(sslReq[0:4], clientProtocol41|clientSSL|clientSecureConnection|clientPluginAuth)
	binary.LittleEndian.PutUint32(sslReq[4:8], 16<<20)
	sslReq[8] = 0x21
	if err := writePacket(raw, 1, sslReq); err != nil {
		t.Fatal(err)
	}
	tlsConn := tls.Client(raw, &tls.Config{InsecureSkipVerify: true, ServerName: sni})
	if err := tlsConn.Handshake(); err != nil {
		t.Fatal(err)
	}
	return tlsConn, g.Salt
}

func fakeMySQLBackend(t *testing.T, password string) net.Listener {
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
				salt := bytes.Repeat([]byte{0x42}, 20)
				if err := writePacket(c, 0, buildGreeting(1, salt)); err != nil {
					return
				}
				seq, payload, err := readPacket(c)
				if err != nil {
					return
				}
				resp, err := parseHandshakeResponse(payload)
				if err != nil || !verifyNative(resp.Auth, salt, password) {
					_ = writePacket(c, seq+1, errPacket(errAccessDenied, "28000", "denied"))
					return
				}
				if err := writePacket(c, seq+1, okPacket()); err != nil {
					return
				}
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()
	return ln
}
