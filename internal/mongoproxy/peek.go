package mongoproxy

import (
	"encoding/binary"
	"fmt"
	"io"
)

const maxHandshake = 16 << 10

// peekClientHelloSNI reads the first TLS record and returns the SNI plus the
// raw bytes so they can be forwarded to the backend (passthrough).
func peekClientHelloSNI(r io.Reader) (sni string, record []byte, err error) {
	hdr := make([]byte, 5)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return "", nil, err
	}
	if hdr[0] != 0x16 {
		return "", hdr, fmt.Errorf("not a TLS handshake (first byte 0x%02x)", hdr[0])
	}
	n := int(binary.BigEndian.Uint16(hdr[3:5]))
	if n < 4 || n > maxHandshake {
		return "", hdr, fmt.Errorf("invalid TLS record length %d", n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return "", append(hdr, body[:0]...), err
	}
	record = append(hdr, body...)
	sni, err = sniFromHandshake(body)
	return sni, record, err
}

func sniFromHandshake(body []byte) (string, error) {
	if len(body) < 4 || body[0] != 0x01 {
		return "", fmt.Errorf("not a TLS ClientHello")
	}
	helloLen := int(body[1])<<16 | int(body[2])<<8 | int(body[3])
	if helloLen < 0 || 4+helloLen > len(body) {
		return "", fmt.Errorf("truncated ClientHello")
	}
	p := body[4 : 4+helloLen]
	// client_version (2) + random (32)
	if len(p) < 34 {
		return "", fmt.Errorf("short ClientHello")
	}
	p = p[34:]
	if len(p) < 1 {
		return "", fmt.Errorf("short session id")
	}
	sid := int(p[0])
	p = p[1:]
	if sid > len(p) {
		return "", fmt.Errorf("bad session id")
	}
	p = p[sid:]
	if len(p) < 2 {
		return "", fmt.Errorf("short cipher suites")
	}
	cs := int(binary.BigEndian.Uint16(p[:2]))
	p = p[2:]
	if cs > len(p) {
		return "", fmt.Errorf("bad cipher suites")
	}
	p = p[cs:]
	if len(p) < 1 {
		return "", fmt.Errorf("short compression")
	}
	comp := int(p[0])
	p = p[1:]
	if comp > len(p) {
		return "", fmt.Errorf("bad compression")
	}
	p = p[comp:]
	if len(p) == 0 {
		return "", fmt.Errorf("missing TLS extensions / SNI")
	}
	if len(p) < 2 {
		return "", fmt.Errorf("short extensions length")
	}
	extLen := int(binary.BigEndian.Uint16(p[:2]))
	p = p[2:]
	if extLen > len(p) {
		extLen = len(p)
	}
	p = p[:extLen]
	for len(p) >= 4 {
		typ := binary.BigEndian.Uint16(p[0:2])
		n := int(binary.BigEndian.Uint16(p[2:4]))
		p = p[4:]
		if n > len(p) {
			return "", fmt.Errorf("truncated extension")
		}
		data := p[:n]
		p = p[n:]
		if typ != 0 {
			continue
		}
		return parseServerName(data)
	}
	return "", fmt.Errorf("missing TLS server name")
}

func parseServerName(data []byte) (string, error) {
	if len(data) < 2 {
		return "", fmt.Errorf("short SNI list")
	}
	listLen := int(binary.BigEndian.Uint16(data[:2]))
	data = data[2:]
	if listLen > len(data) {
		listLen = len(data)
	}
	data = data[:listLen]
	for len(data) >= 3 {
		nameType := data[0]
		n := int(binary.BigEndian.Uint16(data[1:3]))
		data = data[3:]
		if n > len(data) {
			return "", fmt.Errorf("truncated SNI name")
		}
		if nameType == 0 {
			return string(data[:n]), nil
		}
		data = data[n:]
	}
	return "", fmt.Errorf("no hostname in SNI")
}
