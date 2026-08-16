package mysqlproxy

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"sync/atomic"
	"time"

	"github.com/adityaraj/sprout/internal/mysql"
	"github.com/adityaraj/sprout/internal/postgres"
)

// Server greets as MySQL, upgrades to TLS for SNI, verifies native password,
// logs into the local mysqld, then splices the command phase.
type Server struct {
	Addr      string
	TLSConfig *tls.Config
	Resolve   Resolver
	ln        net.Listener
	connID    uint32
}

// Listen binds Addr. Serve must be called afterwards.
func (s *Server) Listen() error {
	if s.TLSConfig == nil {
		return fmt.Errorf("mysqlproxy: TLS config required")
	}
	if s.Resolve == nil {
		return fmt.Errorf("mysqlproxy: resolver required")
	}
	ln, err := net.Listen("tcp", s.Addr)
	if err != nil {
		return fmt.Errorf("mysqlproxy listen %s: %w (need CAP_NET_BIND_SERVICE for :3306, or set SPROUT_MYSQL_PROXY_PORT)", s.Addr, err)
	}
	s.ln = ln
	fmt.Fprintf(os.Stderr, "mysqlproxy: hostname router on %s → local mysqld via TLS server name\n", ln.Addr())
	return nil
}

// Serve accepts connections until Close. Listen must have succeeded.
func (s *Server) Serve() error {
	if s.ln == nil {
		return fmt.Errorf("mysqlproxy: Listen first")
	}
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return err
		}
		go s.handle(c)
	}
}

func (s *Server) ListenAndServe() error {
	if err := s.Listen(); err != nil {
		return err
	}
	return s.Serve()
}

func (s *Server) BoundAddr() string {
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

func (s *Server) Close() error {
	if s.ln != nil {
		return s.ln.Close()
	}
	return nil
}

func (s *Server) handle(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(20 * time.Second))

	salt := make([]byte, 20)
	if _, err := rand.Read(salt); err != nil {
		return
	}
	if err := writePacket(c, 0, buildGreeting(atomic.AddUint32(&s.connID, 1), salt)); err != nil {
		return
	}

	seq, payload, err := readPacket(c)
	if err != nil || len(payload) < 4 {
		return
	}
	caps := binary.LittleEndian.Uint32(payload[0:4])
	if caps&clientSSL == 0 {
		writeERR(c, seq+1, errSecureTransport, "HY000", "SSL required. Use --ssl-mode=REQUIRED")
		return
	}

	tlsCfg := s.TLSConfig.Clone()
	tlsConn := tls.Server(c, tlsCfg)
	if err := tlsConn.Handshake(); err != nil {
		return
	}
	sni := tlsConn.ConnectionState().ServerName

	seq, payload, err = readPacket(tlsConn)
	if err != nil {
		return
	}
	resp, err := parseHandshakeResponse(payload)
	if err != nil {
		writeERR(tlsConn, seq+1, errUnknown, "HY000", "malformed handshake")
		return
	}
	replySeq := seq + 1
	if !wantsNative(resp.Plugin) {
		if err := writePacket(tlsConn, replySeq, authSwitchNative(salt)); err != nil {
			return
		}
		seq, payload, err = readPacket(tlsConn)
		if err != nil {
			return
		}
		resp.Auth = payload
		if len(resp.Auth) == 21 && resp.Auth[20] == 0 {
			resp.Auth = resp.Auth[:20]
		}
		replySeq = seq + 1
	}

	target, err := s.Resolve(sni)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mysqlproxy: %v\n", err)
		writeERR(tlsConn, replySeq, errUnknown, "HY000", err.Error())
		return
	}
	if !verifyNative(resp.Auth, salt, target.Password) {
		writeERR(tlsConn, replySeq, errAccessDenied, "28000", "Access denied (using password: YES)")
		return
	}

	backend, err := net.DialTimeout("tcp", net.JoinHostPort(target.Host, fmt.Sprintf("%d", target.Port)), 5*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mysqlproxy: dial %s:%d: %v\n", target.Host, target.Port, err)
		writeERR(tlsConn, replySeq, errConnRefused, "HY000", "cannot reach MySQL instance")
		return
	}
	defer backend.Close()

	if err := loginBackend(backend, postgres.DBUser(), target.Password, resp.Database); err != nil {
		fmt.Fprintf(os.Stderr, "mysqlproxy: backend login: %v\n", err)
		writeERR(tlsConn, replySeq, errAccessDenied, "28000", "backend login failed")
		return
	}
	if err := writePacket(tlsConn, replySeq, okPacket()); err != nil {
		return
	}

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

// ListenAddr is ":3306" unless SPROUT_MYSQL_PROXY_LISTEN or SPROUT_MYSQL_PROXY_PORT is set.
func ListenAddr() string {
	if a := os.Getenv("SPROUT_MYSQL_PROXY_LISTEN"); a != "" {
		return a
	}
	return fmt.Sprintf(":%d", mysql.ProxyPort())
}
