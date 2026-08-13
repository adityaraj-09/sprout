package pgproxy

import (
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/adityaraj/sprout/internal/postgres"
)

const (
	sslRequestCode = 80877103
	gssRequestCode = 80877104
)

// Server terminates Postgres SSLRequest + TLS, then splices to the backend
// selected from SNI (test-x.example.com → 127.0.0.2:<instance port>).
type Server struct {
	Addr      string
	TLSConfig *tls.Config
	Resolve   Resolver
	ln        net.Listener
}

// Listen binds Addr. Serve must be called afterwards.
func (s *Server) Listen() error {
	if s.TLSConfig == nil {
		return fmt.Errorf("pgproxy: TLS config required")
	}
	if s.Resolve == nil {
		return fmt.Errorf("pgproxy: resolver required")
	}
	ln, err := net.Listen("tcp", s.Addr)
	if err != nil {
		return fmt.Errorf("pgproxy listen %s: %w (need CAP_NET_BIND_SERVICE for :5432, or set SPROUT_PG_PROXY_PORT)", s.Addr, err)
	}
	s.ln = ln
	fmt.Fprintf(os.Stderr, "pgproxy: SNI router on %s → backends via TLS server name\n", ln.Addr())
	return nil
}

// Serve accepts connections until Close. Listen must have succeeded.
func (s *Server) Serve() error {
	if s.ln == nil {
		return fmt.Errorf("pgproxy: Listen first")
	}
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return err
		}
		go s.handle(c)
	}
}

// ListenAndServe binds Addr and accepts until the listener is closed.
func (s *Server) ListenAndServe() error {
	if err := s.Listen(); err != nil {
		return err
	}
	return s.Serve()
}

// BoundAddr is the actual listen address after Listen.
func (s *Server) BoundAddr() string {
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// Close stops the listener.
func (s *Server) Close() error {
	if s.ln != nil {
		return s.ln.Close()
	}
	return nil
}

func (s *Server) handle(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))
	var hdr [8]byte
	if _, err := io.ReadFull(c, hdr[:]); err != nil {
		return
	}
	n := binary.BigEndian.Uint32(hdr[0:4])
	code := binary.BigEndian.Uint32(hdr[4:8])
	if n == 8 && code == gssRequestCode {
		_, _ = c.Write([]byte{'N'})
		return
	}
	if n != 8 || code != sslRequestCode {
		return
	}
	if _, err := c.Write([]byte{'S'}); err != nil {
		return
	}
	tlsCfg := s.TLSConfig.Clone()
	tlsConn := tls.Server(c, tlsCfg)
	if err := tlsConn.Handshake(); err != nil {
		return
	}
	sni := tlsConn.ConnectionState().ServerName
	host, port, err := s.Resolve(sni)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pgproxy: %v\n", err)
		return
	}
	backend, err := net.DialTimeout("tcp", net.JoinHostPort(host, fmt.Sprintf("%d", port)), 5*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pgproxy: dial %s:%d: %v\n", host, port, err)
		return
	}
	defer backend.Close()
	_ = c.SetDeadline(time.Time{})
	_ = backend.SetDeadline(time.Time{})
	errc := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(backend, tlsConn)
		errc <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(tlsConn, backend)
		errc <- struct{}{}
	}()
	<-errc
}

// ListenAddr is ":5432" unless SPROUT_PG_PROXY_LISTEN or SPROUT_PG_PROXY_PORT is set.
func ListenAddr() string {
	if a := os.Getenv("SPROUT_PG_PROXY_LISTEN"); a != "" {
		return a
	}
	return fmt.Sprintf(":%d", postgres.ProxyPort())
}
