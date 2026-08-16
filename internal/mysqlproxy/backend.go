package mysqlproxy

import (
	"fmt"
	"net"
)

func loginBackend(conn net.Conn, user, password, database string) error {
	_, payload, err := readPacket(conn)
	if err != nil {
		return fmt.Errorf("backend greeting: %w", err)
	}
	g, err := parseGreeting(payload)
	if err != nil {
		return fmt.Errorf("backend greeting: %w", err)
	}
	plugin := g.Plugin
	salt := g.Salt
	if !wantsNative(plugin) {
		// Prefer native; the server may AuthSwitch after we send a native response.
		plugin = nativePlugin
	}
	scramble := nativePassword(password, salt)
	resp := buildHandshakeResponse(user, database, scramble, nativePlugin)
	if err := writePacket(conn, 1, resp); err != nil {
		return err
	}
	seq := byte(2)
	for i := 0; i < 4; i++ {
		_, payload, err = readPacket(conn)
		if err != nil {
			return fmt.Errorf("backend auth: %w", err)
		}
		if len(payload) == 0 {
			return fmt.Errorf("backend auth: empty packet")
		}
		switch payload[0] {
		case 0x00:
			return nil
		case 0xff:
			return parseERR(payload)
		case 0xfe:
			if len(payload) < 9 {
				return nil // EOF
			}
			plugin, salt, err = parseAuthSwitch(payload)
			if err != nil {
				return err
			}
			if !wantsNative(plugin) {
				return fmt.Errorf("backend wants %s; set default_authentication_plugin=mysql_native_password", plugin)
			}
			if err := writePacket(conn, seq, nativePassword(password, salt)); err != nil {
				return err
			}
			seq++
		case 0x01:
			return fmt.Errorf("backend sent auth more-data (caching_sha2); set mysql_native_password")
		default:
			return fmt.Errorf("unexpected backend packet 0x%02x", payload[0])
		}
	}
	return fmt.Errorf("backend auth did not complete")
}
