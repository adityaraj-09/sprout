package postgres

import (
	"net"
	"strconv"
	"strings"
	"testing"
)

func TestPortListeningAndEnsurePortFree(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if !PortListening(port) {
		t.Fatalf("expected port %d in use", port)
	}
	err = EnsurePortFree(port)
	if err == nil {
		t.Fatal("expected busy port error")
	}
	if !strings.Contains(err.Error(), "mongod") || !strings.Contains(err.Error(), strconv.Itoa(port)) {
		t.Fatalf("error should mention mongod and the port, got %v", err)
	}
	_ = ln.Close()
	if PortListening(0) {
		t.Fatal("port 0 is not a listener")
	}
}
