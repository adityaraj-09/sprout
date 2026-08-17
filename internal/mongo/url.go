package mongo

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/adityaraj/sprout/internal/postgres"
)

// Conn is a parsed mongodb:// or mongodb+srv:// URL.
type Conn struct {
	Host     string
	Port     int
	User     string
	Password string
	Database string
	TLS      bool
	SRV      bool
	Raw      string
}

func ParseURL(raw string) (Conn, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return Conn{}, fmt.Errorf("invalid url: %w", err)
	}
	srv := false
	switch strings.ToLower(u.Scheme) {
	case "mongodb":
	case "mongodb+srv":
		srv = true
	default:
		return Conn{}, fmt.Errorf("url scheme must be mongodb/mongodb+srv")
	}
	host := u.Hostname()
	if host == "" {
		host = "127.0.0.1"
	}
	port := 27017
	if p := u.Port(); p != "" {
		port, _ = strconv.Atoi(p)
	}
	user := ""
	pass := ""
	if u.User != nil {
		user = u.User.Username()
		pass, _ = u.User.Password()
	}
	db := strings.TrimPrefix(u.Path, "/")
	if i := strings.IndexByte(db, '?'); i >= 0 {
		db = db[:i]
	}
	tls := srv
	q := u.Query()
	switch strings.ToLower(q.Get("tls")) {
	case "true", "1", "yes":
		tls = true
	case "false", "0", "no":
		tls = false
	}
	if strings.EqualFold(q.Get("ssl"), "true") {
		tls = true
	}
	return Conn{Host: host, Port: port, User: user, Password: pass, Database: db, TLS: tls, SRV: srv, Raw: raw}, nil
}

func (c Conn) dumpURI() string {
	if strings.TrimSpace(c.Raw) != "" {
		return c.Raw
	}
	u := url.URL{Scheme: "mongodb", Host: net.JoinHostPort(c.Host, strconv.Itoa(c.Port))}
	if c.SRV {
		u.Scheme = "mongodb+srv"
		u.Host = c.Host
	}
	if c.User != "" {
		if c.Password != "" {
			u.User = url.UserPassword(c.User, c.Password)
		} else {
			u.User = url.User(c.User)
		}
	}
	if c.Database != "" {
		u.Path = "/" + c.Database
	}
	return u.String()
}

// FormatConnString builds a mongodb:// URL on the unique instance port with tls=true.
func FormatConnString(port int, db, password, name, from string, owner ...string) string {
	own := ""
	if len(owner) > 0 {
		own = owner[0]
	}
	host := postgres.AdvertiseHost(name, from, own)
	u := url.URL{
		Scheme: "mongodb",
		Host:   net.JoinHostPort(host, strconv.Itoa(port)),
	}
	if db != "" {
		u.Path = "/" + db
	}
	user := postgres.DBUser()
	if password != "" {
		u.User = url.UserPassword(user, password)
	} else {
		u.User = url.User(user)
	}
	q := u.Query()
	q.Set("tls", "true")
	q.Set("tlsAllowInvalidCertificates", "true")
	q.Set("authSource", "admin")
	u.RawQuery = q.Encode()
	return u.String()
}

func MongoshOneLiner(port int, password, name, from string, owner ...string) string {
	return fmt.Sprintf(`mongosh "%s"`, FormatConnString(port, "", password, name, from, owner...))
}
