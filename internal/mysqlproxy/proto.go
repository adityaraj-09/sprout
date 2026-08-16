package mysqlproxy

import (
	"crypto/sha1"
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
)

const (
	clientLongPassword     = 1 << 0
	clientFoundRows        = 1 << 1
	clientLongFlag         = 1 << 2
	clientConnectWithDB    = 1 << 3
	clientProtocol41       = 1 << 9
	clientSSL              = 1 << 11
	clientTransactions     = 1 << 13
	clientSecureConnection = 1 << 15
	clientPluginAuth       = 1 << 19
	clientPluginAuthLenEnc = 1 << 21

	nativePlugin = "mysql_native_password"

	errSecureTransport = 3159
	errAccessDenied    = 1045
	errUnknown         = 1105
	errConnRefused     = 2003
)

type handshakeResp struct {
	Caps     uint32
	User     string
	Auth     []byte
	Database string
	Plugin   string
}

type greeting struct {
	Salt   []byte
	Plugin string
	Caps   uint32
}

func readPacket(r io.Reader) (seq byte, payload []byte, err error) {
	var hdr [4]byte
	if _, err = io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := int(uint32(hdr[0]) | uint32(hdr[1])<<8 | uint32(hdr[2])<<16)
	seq = hdr[3]
	if n == 0 {
		return seq, nil, nil
	}
	payload = make([]byte, n)
	_, err = io.ReadFull(r, payload)
	return seq, payload, err
}

func writePacket(w io.Writer, seq byte, payload []byte) error {
	var hdr [4]byte
	n := len(payload)
	hdr[0] = byte(n)
	hdr[1] = byte(n >> 8)
	hdr[2] = byte(n >> 16)
	hdr[3] = seq
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	_, err := w.Write(payload)
	return err
}

func nativePassword(password string, salt []byte) []byte {
	if password == "" {
		return []byte{}
	}
	if len(salt) > 20 {
		salt = salt[:20]
	}
	stage1 := sha1.Sum([]byte(password))
	stage2 := sha1.Sum(stage1[:])
	h := sha1.New()
	h.Write(salt)
	h.Write(stage2[:])
	stage3 := h.Sum(nil)
	out := make([]byte, 20)
	for i := 0; i < 20; i++ {
		out[i] = stage1[i] ^ stage3[i]
	}
	return out
}

func verifyNative(scramble, salt []byte, password string) bool {
	want := nativePassword(password, salt)
	if len(want) == 0 {
		return len(scramble) == 0
	}
	if len(scramble) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare(scramble, want) == 1
}

func buildGreeting(connID uint32, salt []byte) []byte {
	if len(salt) < 20 {
		padded := make([]byte, 20)
		copy(padded, salt)
		salt = padded
	}
	salt = salt[:20]
	caps := uint32(clientLongPassword | clientFoundRows | clientLongFlag | clientConnectWithDB |
		clientProtocol41 | clientSSL | clientTransactions | clientSecureConnection |
		clientPluginAuth | clientPluginAuthLenEnc)

	b := make([]byte, 0, 80)
	b = append(b, 0x0a)
	b = append(b, []byte("8.0.36-sprout")...)
	b = append(b, 0)
	b = binary.LittleEndian.AppendUint32(b, connID)
	b = append(b, salt[:8]...)
	b = append(b, 0)
	b = binary.LittleEndian.AppendUint16(b, uint16(caps))
	b = append(b, 0x21)
	b = binary.LittleEndian.AppendUint16(b, 0x0002)
	b = binary.LittleEndian.AppendUint16(b, uint16(caps>>16))
	b = append(b, 21)
	b = append(b, make([]byte, 10)...)
	b = append(b, salt[8:20]...)
	b = append(b, 0)
	b = append(b, []byte(nativePlugin)...)
	b = append(b, 0)
	return b
}

func parseGreeting(p []byte) (greeting, error) {
	if len(p) < 1 {
		return greeting{}, fmt.Errorf("empty greeting")
	}
	if p[0] == 0xff {
		return greeting{}, parseERR(p)
	}
	if p[0] != 0x0a {
		return greeting{}, fmt.Errorf("unsupported protocol 0x%02x", p[0])
	}
	i := 1
	_, i = readCString(p, i)
	if i+4+8+1+2 > len(p) {
		return greeting{}, fmt.Errorf("short greeting")
	}
	i += 4
	part1 := p[i : i+8]
	i += 8
	i++ // filler
	caps := uint32(binary.LittleEndian.Uint16(p[i : i+2]))
	i += 2
	g := greeting{Caps: caps, Plugin: nativePlugin}
	if i >= len(p) {
		g.Salt = append([]byte(nil), part1...)
		return g, nil
	}
	i++ // charset
	if i+2 > len(p) {
		g.Salt = append([]byte(nil), part1...)
		return g, nil
	}
	i += 2 // status
	if i+2 > len(p) {
		g.Salt = append([]byte(nil), part1...)
		return g, nil
	}
	caps |= uint32(binary.LittleEndian.Uint16(p[i:i+2])) << 16
	g.Caps = caps
	i += 2
	authLen := 21
	if i < len(p) {
		if p[i] != 0 {
			authLen = int(p[i])
		}
		i++
	}
	if i+10 <= len(p) {
		i += 10
	}
	part2Len := 13
	if authLen > 8 {
		part2Len = authLen - 8
	}
	if i+part2Len > len(p) {
		part2Len = len(p) - i
	}
	if part2Len < 0 {
		part2Len = 0
	}
	part2 := p[i : i+part2Len]
	i += part2Len
	salt := append(append([]byte(nil), part1...), part2...)
	for len(salt) > 0 && salt[len(salt)-1] == 0 {
		salt = salt[:len(salt)-1]
	}
	if len(salt) > 20 {
		salt = salt[:20]
	}
	g.Salt = salt
	if caps&clientPluginAuth != 0 && i < len(p) {
		plugin, _ := readCString(p, i)
		if plugin != "" {
			g.Plugin = plugin
		}
	}
	return g, nil
}

func parseHandshakeResponse(p []byte) (handshakeResp, error) {
	if len(p) < 32 {
		return handshakeResp{}, fmt.Errorf("short handshake response")
	}
	caps := binary.LittleEndian.Uint32(p[0:4])
	i := 32
	user, i := readCString(p, i)
	var auth []byte
	switch {
	case caps&clientPluginAuthLenEnc != 0:
		n, ni, err := readLenEnc(p, i)
		if err != nil {
			return handshakeResp{}, err
		}
		i = ni
		if i+int(n) > len(p) {
			return handshakeResp{}, fmt.Errorf("short auth-response")
		}
		auth = append([]byte(nil), p[i:i+int(n)]...)
		i += int(n)
	case caps&clientSecureConnection != 0:
		if i >= len(p) {
			return handshakeResp{}, fmt.Errorf("short auth-response")
		}
		n := int(p[i])
		i++
		if i+n > len(p) {
			return handshakeResp{}, fmt.Errorf("short auth-response")
		}
		auth = append([]byte(nil), p[i:i+n]...)
		i += n
	default:
		auth, i = readCBytes(p, i)
	}
	db := ""
	if caps&clientConnectWithDB != 0 && i < len(p) {
		db, i = readCString(p, i)
	}
	plugin := ""
	if caps&clientPluginAuth != 0 && i < len(p) {
		plugin, _ = readCString(p, i)
	}
	return handshakeResp{Caps: caps, User: user, Auth: auth, Database: db, Plugin: plugin}, nil
}

func buildHandshakeResponse(user, db string, scramble []byte, plugin string) []byte {
	caps := uint32(clientLongPassword | clientLongFlag | clientProtocol41 |
		clientTransactions | clientSecureConnection | clientPluginAuth | clientPluginAuthLenEnc)
	if db != "" {
		caps |= clientConnectWithDB
	}
	b := make([]byte, 0, 64+len(user)+len(db)+len(scramble)+len(plugin))
	b = binary.LittleEndian.AppendUint32(b, caps)
	b = binary.LittleEndian.AppendUint32(b, 16<<20)
	b = append(b, 0x21)
	b = append(b, make([]byte, 23)...)
	b = append(b, user...)
	b = append(b, 0)
	b = appendLenEncInt(b, len(scramble))
	b = append(b, scramble...)
	if db != "" {
		b = append(b, db...)
		b = append(b, 0)
	}
	if plugin == "" {
		plugin = nativePlugin
	}
	b = append(b, plugin...)
	b = append(b, 0)
	return b
}

func authSwitchNative(salt []byte) []byte {
	if len(salt) > 20 {
		salt = salt[:20]
	}
	b := []byte{0xfe}
	b = append(b, nativePlugin...)
	b = append(b, 0)
	b = append(b, salt...)
	b = append(b, 0)
	return b
}

func parseAuthSwitch(p []byte) (plugin string, salt []byte, err error) {
	if len(p) < 1 || p[0] != 0xfe {
		return "", nil, fmt.Errorf("not an auth switch")
	}
	if len(p) < 9 {
		return "", nil, fmt.Errorf("eof, not auth switch")
	}
	plugin, i := readCString(p, 1)
	salt = append([]byte(nil), p[i:]...)
	for len(salt) > 0 && salt[len(salt)-1] == 0 {
		salt = salt[:len(salt)-1]
	}
	if len(salt) > 20 {
		salt = salt[:20]
	}
	return plugin, salt, nil
}

func okPacket() []byte {
	return []byte{0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00}
}

func errPacket(code uint16, state, msg string) []byte {
	if len(state) != 5 {
		state = "HY000"
	}
	b := []byte{0xff, byte(code), byte(code >> 8), '#'}
	b = append(b, state...)
	b = append(b, msg...)
	return b
}

func parseERR(p []byte) error {
	if len(p) < 3 || p[0] != 0xff {
		return fmt.Errorf("mysql error")
	}
	code := binary.LittleEndian.Uint16(p[1:3])
	msg := ""
	if len(p) > 9 && p[3] == '#' {
		msg = string(p[9:])
	} else if len(p) > 3 {
		msg = string(p[3:])
	}
	return fmt.Errorf("mysql error %d: %s", code, msg)
}

func writeERR(w io.Writer, seq byte, code uint16, state, msg string) {
	_ = writePacket(w, seq, errPacket(code, state, msg))
}

func readCString(p []byte, i int) (string, int) {
	s, j := readCBytes(p, i)
	return string(s), j
}

func readCBytes(p []byte, i int) ([]byte, int) {
	if i >= len(p) {
		return nil, i
	}
	j := i
	for j < len(p) && p[j] != 0 {
		j++
	}
	out := append([]byte(nil), p[i:j]...)
	if j < len(p) {
		j++
	}
	return out, j
}

func readLenEnc(p []byte, i int) (uint64, int, error) {
	if i >= len(p) {
		return 0, i, fmt.Errorf("short lenenc")
	}
	switch p[i] {
	case 0xfc:
		if i+3 > len(p) {
			return 0, i, fmt.Errorf("short lenenc")
		}
		return uint64(p[i+1]) | uint64(p[i+2])<<8, i + 3, nil
	case 0xfd:
		if i+4 > len(p) {
			return 0, i, fmt.Errorf("short lenenc")
		}
		return uint64(p[i+1]) | uint64(p[i+2])<<8 | uint64(p[i+3])<<16, i + 4, nil
	case 0xfe:
		if i+9 > len(p) {
			return 0, i, fmt.Errorf("short lenenc")
		}
		return binary.LittleEndian.Uint64(p[i+1 : i+9]), i + 9, nil
	case 0xfb:
		return 0, i + 1, nil
	default:
		return uint64(p[i]), i + 1, nil
	}
}

func appendLenEncInt(b []byte, n int) []byte {
	if n < 251 {
		return append(b, byte(n))
	}
	if n < 1<<16 {
		return append(b, 0xfc, byte(n), byte(n>>8))
	}
	return append(b, 0xfd, byte(n), byte(n>>8), byte(n>>16))
}

func wantsNative(plugin string) bool {
	p := strings.ToLower(strings.TrimSpace(plugin))
	return p == "" || p == nativePlugin
}
