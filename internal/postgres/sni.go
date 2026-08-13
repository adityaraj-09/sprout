package postgres

import "strings"

// NormalizeSNI lowercases and strips a trailing dot from a TLS server name.
func NormalizeSNI(sni string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(sni), "."))
}

// MatchesSNI reports whether a TLS server name refers to this instance.
func MatchesSNI(sni, name, from string) bool {
	sni = NormalizeSNI(sni)
	if sni == "" {
		return false
	}
	if sni == strings.ToLower(AdvertiseHost(name, from)) {
		return true
	}
	return sni == HostLabel(name, from)
}
