package mongoproxy

import (
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/adityaraj/sprout/internal/mongo"
	"github.com/adityaraj/sprout/internal/pgproxy"
	"github.com/adityaraj/sprout/internal/postgres"
)

// Server is a TLS SNI passthrough on :27017. It peeks the ClientHello server
// name, then splices the TCP stream to the matching local mongod (which
// terminates TLS). Unique instance ports stay on loopback.
type Server struct {
	Addr    string
	Resolve pgproxy.Resolver
	ln      net.Listener
}

func (s *Server) Listen() error {
	if s.Resolve == nil {
		return fmt.Errorf("mongoproxy: resolver required")
	}
	if s.Addr == "" {
		s.Addr = mongo.ListenAddr()
	}
	ln, err := net.Listen("tcp", s.Addr)
	if err != nil {
		return fmt.Errorf("mongoproxy listen %s: %w (need CAP_NET_BIND_SERVICE for :27017, or set SPROUT_MONGO_PROXY_PORT)", s.Addr, err)
	}
	s.ln = ln
	fmt.Fprintf(os.Stderr, "mongoproxy: SNI passthrough on %s → local mongod via TLS server name\n", ln.Addr())
	return nil
}

func (s *Server) Serve() error {
	if s.ln == nil {
		return fmt.Errorf("mongoproxy: Listen first")
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
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))
	sni, hello, err := peekClientHelloSNI(c)
	if err != nil {
		return
	}
	sni = postgres.NormalizeSNI(sni)
	host, port, err := s.Resolve(sni)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mongoproxy: %v\n", err)
		return
	}
	backend, err := net.DialTimeout("tcp", net.JoinHostPort(host, fmt.Sprintf("%d", port)), 5*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mongoproxy: dial %s:%d: %v\n", host, port, err)
		return
	}
	defer backend.Close()
	if _, err := backend.Write(hello); err != nil {
		return
	}
	_ = c.SetDeadline(time.Time{})
	_ = backend.SetDeadline(time.Time{})
	errc := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(backend, c)
		errc <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(c, backend)
		errc <- struct{}{}
	}()
	<-errc
}
