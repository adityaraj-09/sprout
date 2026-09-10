package qdrant

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/adityaraj/sprout/internal/postgres"
)

// Conn is a parsed qdrant://, qdrants://, http://, or https:// URL.
type Conn struct {
	Host     string
	Port     int
	APIKey   string
	TLS      bool
	Insecure bool
	Raw      string
}

func ParseURL(raw string) (Conn, error) {
	trimmed := strings.TrimSpace(raw)
	u, err := url.Parse(trimmed)
	if err != nil {
		return Conn{}, fmt.Errorf("invalid url: %w", err)
	}
	tlsOn := false
	switch strings.ToLower(u.Scheme) {
	case "qdrant", "http":
	case "qdrants", "https":
		tlsOn = true
	default:
		return Conn{}, fmt.Errorf("url scheme must be qdrant/qdrants/http/https")
	}
	host := u.Hostname()
	if host == "" {
		host = "127.0.0.1"
	}
	port := 6333
	if p := u.Port(); p != "" {
		port, _ = strconv.Atoi(p)
	}
	key := ""
	if u.User != nil {
		if p, ok := u.User.Password(); ok && p != "" {
			key = p
		} else if u.User.Username() != "" {
			key = u.User.Username()
		}
	}
	q := u.Query()
	for _, name := range []string{"api-key", "api_key", "apiKey"} {
		if v := q.Get(name); v != "" {
			key = v
			break
		}
	}
	switch strings.ToLower(q.Get("tls")) {
	case "true", "1", "yes":
		tlsOn = true
	case "false", "0", "no":
		tlsOn = false
	}
	insecure := false
	switch strings.ToLower(q.Get("tlsAllowInvalidCertificates")) {
	case "true", "1", "yes":
		insecure = true
	}
	if strings.EqualFold(q.Get("tlsInsecure"), "true") {
		insecure = true
	}
	return Conn{Host: host, Port: port, APIKey: key, TLS: tlsOn, Insecure: insecure, Raw: trimmed}, nil
}

func (c Conn) BaseURL() string {
	scheme := "http"
	if c.TLS {
		scheme = "https"
	}
	return scheme + "://" + net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

// FormatConnString builds an http(s) URL. When the SNI proxy is on, the advertised
// port is 6333 and the scheme is https; otherwise the instance port (http).
func FormatConnString(port int, password, name, from string, owner ...string) string {
	own := ""
	if len(owner) > 0 {
		own = owner[0]
	}
	host := postgres.AdvertiseHost(name, from, own)
	scheme := "http"
	if useTLS() {
		scheme = "https"
	}
	u := url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(host, strconv.Itoa(AdvertisePort(port))),
	}
	q := u.Query()
	if password != "" {
		q.Set("api-key", password)
	}
	if useTLS() {
		q.Set("tlsAllowInvalidCertificates", "true")
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func CurlOneLiner(port int, password, name, from string, owner ...string) string {
	u := FormatConnString(port, "", name, from, owner...)
	insecure := ""
	if strings.HasPrefix(u, "https://") {
		insecure = "-k "
	}
	header := ""
	if password != "" {
		header = fmt.Sprintf("-H 'api-key: %s' ", password)
	}
	return fmt.Sprintf("curl -sS %s%s%s/collections", insecure, header, strings.TrimRight(u, "/"))
}
