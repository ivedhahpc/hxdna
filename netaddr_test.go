package hxdna

import (
	"net"
	"testing"
)

func TestOutboundIP(t *testing.T) {
	// A loopback target identifies nothing: "".
	for _, target := range []string{"nats://127.0.0.1:4222", "https://localhost:3443/api", "127.0.0.1:4222"} {
		if got := OutboundIP(target); got != "" {
			t.Errorf("%s → %q, want empty for loopback", target, got)
		}
	}
	// Unparseable or unresolvable: "".
	if got := OutboundIP("::::"); got != "" {
		t.Errorf("garbage → %q", got)
	}
	// A routable target, in every form a worker holds, gives this machine's address (or "" on a
	// box with no route out — either way never a loopback, and always an IP).
	for _, target := range []string{"tls://192.0.2.10:4224", "https://192.0.2.10:3443/api", "192.0.2.10:4224", "192.0.2.10"} {
		got := OutboundIP(target)
		if got == "" {
			continue
		}
		ip := net.ParseIP(got)
		if ip == nil || ip.IsLoopback() {
			t.Errorf("%s → %q, not a usable address", target, got)
		}
	}
}
