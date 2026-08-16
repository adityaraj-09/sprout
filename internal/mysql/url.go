package mysql

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/adityaraj/sprout/internal/postgres"
)

// Conn is a parsed mysql:// or mariadb:// URL.
type Conn struct {
	Host     string
	Port     int
	User     string
	Password string
	Database string
	SSLMode  string
	Raw      string
}

func ParseURL(raw string) (Conn, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return Conn{}, fmt.Errorf("invalid url: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "mysql", "mariadb":
	default:
		return Conn{}, fmt.Errorf("url scheme must be mysql/mariadb")
	}
	host := u.Hostname()
	if host == "" {
		host = "127.0.0.1"
	}
	port := 3306
	if p := u.Port(); p != "" {
		port, _ = strconv.Atoi(p)
	}
	user := "root"
	pass := ""
	if u.User != nil {
		user = u.User.Username()
		pass, _ = u.User.Password()
	}
	db := strings.TrimPrefix(u.Path, "/")
	ssl := u.Query().Get("ssl-mode")
	if ssl == "" {
		ssl = u.Query().Get("sslmode")
	}
	if ssl == "" {
		if host == "127.0.0.1" || host == "localhost" {
			ssl = "DISABLED"
		} else {
			ssl = "REQUIRED"
		}
	}
	return Conn{Host: host, Port: port, User: user, Password: pass, Database: db, SSLMode: ssl, Raw: raw}, nil
}

func (c Conn) pgEnv() []string {
	env := os.Environ()
	if c.Password != "" {
		env = append(env, "MYSQL_PWD="+c.Password)
	}
	return env
}

func (c Conn) sslArg() string {
	m := strings.ToUpper(strings.TrimSpace(c.SSLMode))
	switch m {
	case "", "DISABLE", "DISABLED", "FALSE":
		return "--ssl-mode=DISABLED"
	case "PREFERRED", "PREFER":
		return "--ssl-mode=PREFERRED"
	case "VERIFY_CA", "VERIFY-CA":
		return "--ssl-mode=VERIFY_CA"
	case "VERIFY_IDENTITY", "VERIFY-FULL", "VERIFY_FULL":
		return "--ssl-mode=VERIFY_IDENTITY"
	default:
		return "--ssl-mode=REQUIRED"
	}
}

// FormatConnString builds a mysql:// URL. When the hostname proxy is on,
// the advertised port is 3306 and ssl-mode=REQUIRED so clients send SNI.
func FormatConnString(port int, db, password, name, from string, owner ...string) string {
	own := ""
	if len(owner) > 0 {
		own = owner[0]
	}
	host := postgres.AdvertiseHost(name, from, own)
	u := url.URL{
		Scheme: "mysql",
		Host:   net.JoinHostPort(host, strconv.Itoa(AdvertisePort(port))),
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
	if ProxyEnabled() {
		q := u.Query()
		q.Set("ssl-mode", "REQUIRED")
		u.RawQuery = q.Encode()
	}
	return u.String()
}

func MysqlOneLiner(port int, password, name, from string, owner ...string) string {
	own := ""
	if len(owner) > 0 {
		own = owner[0]
	}
	if ProxyEnabled() {
		return fmt.Sprintf("mysql --ssl-mode=REQUIRED -h %s -P %d -u %s -p%s",
			postgres.AdvertiseHost(name, from, own), AdvertisePort(port), postgres.DBUser(), password)
	}
	return fmt.Sprintf(`mysql "%s"`, FormatConnString(port, "", password, name, from, owner...))
}
