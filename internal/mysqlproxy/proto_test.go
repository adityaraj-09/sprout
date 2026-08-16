package mysqlproxy

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func TestNativePasswordRoundTrip(t *testing.T) {
	salt := []byte("12345678901234567890")
	got := nativePassword("secret", salt)
	if len(got) != 20 {
		t.Fatalf("scramble len %d", len(got))
	}
	if !verifyNative(got, salt, "secret") {
		t.Fatal("expected match")
	}
	if verifyNative(got, salt, "wrong") {
		t.Fatal("wrong password must fail")
	}
	if !verifyNative(nil, salt, "") {
		t.Fatal("empty password")
	}
	if verifyNative(got, salt, "") {
		t.Fatal("empty vs non-empty")
	}
}

func TestGreetingRoundTrip(t *testing.T) {
	salt := bytes.Repeat([]byte{0x41}, 20)
	raw := buildGreeting(7, salt)
	g, err := parseGreeting(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(g.Salt, salt) {
		t.Fatalf("salt %x", g.Salt)
	}
	if g.Plugin != nativePlugin {
		t.Fatalf("plugin %q", g.Plugin)
	}
	if g.Caps&clientSSL == 0 {
		t.Fatal("CLIENT_SSL missing")
	}
}

func TestHandshakeResponseRoundTrip(t *testing.T) {
	scramble := nativePassword("pw", bytes.Repeat([]byte{1}, 20))
	raw := buildHandshakeResponse("sprout", "shop", scramble, nativePlugin)
	resp, err := parseHandshakeResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if resp.User != "sprout" || resp.Database != "shop" || resp.Plugin != nativePlugin {
		t.Fatalf("%+v", resp)
	}
	if !bytes.Equal(resp.Auth, scramble) {
		t.Fatalf("auth %x", resp.Auth)
	}
}

func TestSSLRequestCaps(t *testing.T) {
	p := make([]byte, 32)
	binary.LittleEndian.PutUint32(p[0:4], clientProtocol41|clientSSL)
	if binary.LittleEndian.Uint32(p[0:4])&clientSSL == 0 {
		t.Fatal("ssl flag")
	}
	plain := make([]byte, 32)
	binary.LittleEndian.PutUint32(plain[0:4], clientProtocol41)
	if binary.LittleEndian.Uint32(plain[0:4])&clientSSL != 0 {
		t.Fatal("plain must not have ssl")
	}
}

func TestErrPacketContainsHint(t *testing.T) {
	p := errPacket(errSecureTransport, "HY000", "SSL required. Use --ssl-mode=REQUIRED")
	if p[0] != 0xff {
		t.Fatal("not err")
	}
	if !strings.Contains(string(p), "--ssl-mode=REQUIRED") {
		t.Fatalf("%q", p)
	}
}
