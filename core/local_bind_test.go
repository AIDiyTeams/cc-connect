package core

import (
	"net"
	"strconv"
	"testing"
)

func TestLocalBind_BridgeAndManagementPreserveDefaultAndSupportLoopback(t *testing.T) {
	for _, host := range []string{"", "127.0.0.1", "::1"} {
		t.Run(host, func(t *testing.T) {
			bridge := NewBridgeServer(9810, "test-token", "/bridge/ws", nil)
			bridge.port = 0 // Let the OS allocate a port; do not touch a developer's runtime.
			bridge.SetHost(host)
			bridge.Start()
			defer bridge.Stop()
			if want := net.JoinHostPort(host, strconv.Itoa(0)); bridge.server.Addr != want {
				t.Fatalf("bridge address = %q, want %q", bridge.server.Addr, want)
			}
			management := NewManagementServer(0, "test-token", nil)
			management.SetHost(host)
			management.Start()
			defer management.Stop()
			if want := net.JoinHostPort(host, "0"); management.server.Addr != want {
				t.Fatalf("management address = %q, want %q", management.server.Addr, want)
			}
		})
	}
}
