package postgres

import "strings"

// NormalizeSNI lowercases and strips a trailing dot from a TLS server name.
func NormalizeSNI(sni string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(sni), "."))
}

// MatchesSNI reports whether a TLS server name refers to this instance.
func MatchesSNI(sni, name, from string, owner ...string) bool {
	sni = NormalizeSNI(sni)
	if sni == "" {
		return false
	}
	own := ""
	if len(owner) > 0 {
		own = owner[0]
	}
	if sni == strings.ToLower(AdvertiseHost(name, from, own)) {
		return true
	}
	return sni == HostLabel(name, from, own)
}
