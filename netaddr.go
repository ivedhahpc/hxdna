package hxdna

import (
	"net"
	"net/url"
	"strings"
	"time"
)

// OutboundIP is this machine's address on the network interface it uses to reach target (a URL like
// https://host:3443/api or tls://host:4224, or a host:port) — the address the control plane, and
// usually the machines in the worker's own network, see it connect from. Worked out from the
// routing table: a UDP "dial" sends nothing. "" when it can't tell, or the answer is a loopback
// address (control plane on the same machine), which identifies nothing.
//
// A machine with several networks reports the one towards target; machines reached through another
// interface would see a different address.
func OutboundIP(target string) string {
	host := target
	if u, err := url.Parse(target); err == nil && u.Host != "" {
		host = u.Host
	}
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(strings.Trim(host, "[]"), "443")
	}
	conn, err := net.DialTimeout("udp", host, 2*time.Second)
	if err != nil {
		return ""
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || addr.IP == nil || addr.IP.IsLoopback() || addr.IP.IsUnspecified() {
		return ""
	}
	return addr.IP.String()
}
