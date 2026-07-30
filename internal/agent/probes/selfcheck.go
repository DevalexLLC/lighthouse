package probes

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/net/icmp"
)

// Check is one selfcheck result. Fatal checks must pass for the agent to be
// able to do its job; non-fatal ones are informational (e.g. IPv6 probing on
// a v4-only host).
type Check struct {
	Name   string
	OK     bool
	Detail string
	Fatal  bool
}

// SelfCheck probes the capabilities the M4 probers need so problems surface
// at install time, not as ERROR results at 2 AM. The ICMP prober works with
// EITHER a datagram or a raw socket; traceroute strictly needs raw.
func SelfCheck(stateDir string) []Check {
	var checks []Check

	dgram := trySocket("udp4")
	raw := trySocket("ip4:icmp")
	checks = append(checks,
		Check{
			Name: "icmp (datagram)", OK: dgram == nil, Fatal: false,
			Detail: detailOr(dgram, "unprivileged datagram ICMP available",
				"check net.ipv4.ping_group_range covers the service group"),
		},
		Check{
			Name: "icmp (raw)", OK: raw == nil, Fatal: false,
			Detail: detailOr(raw, "CAP_NET_RAW available",
				"grant CAP_NET_RAW (systemd AmbientCapabilities) for the raw fallback"),
		},
		// The echo prober needs at least one of the two socket modes.
		Check{
			Name: "icmp", OK: dgram == nil || raw == nil, Fatal: true,
			Detail: pick(dgram == nil || raw == nil,
				"echo probing available",
				"no ICMP socket mode works: set ping_group_range or grant CAP_NET_RAW"),
		},
		Check{
			Name: "traceroute", OK: raw == nil, Fatal: false,
			Detail: detailOr(raw, "raw ICMP socket available",
				"traceroute requires a raw ICMP socket (CAP_NET_RAW)"),
		},
	)

	if v6 := trySocket("udp6"); v6 == nil {
		checks = append(checks, Check{Name: "icmp6 (datagram)", OK: true, Detail: "unprivileged datagram ICMPv6 available"})
	} else if v6raw := trySocket("ip6:ipv6-icmp"); v6raw == nil {
		checks = append(checks, Check{Name: "icmp6 (raw)", OK: true, Detail: "raw ICMPv6 available"})
	} else {
		checks = append(checks, Check{
			Name: "icmp6", OK: false, Fatal: false,
			Detail: "no ICMPv6 socket mode works; v6 targets will fail (fine on v4-only hosts)",
		})
	}

	checks = append(checks, spoolCheck(stateDir))
	return checks
}

func trySocket(network string) error {
	conn, err := icmp.ListenPacket(network, "")
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}

func detailOr(err error, ok, remedy string) string {
	if err == nil {
		return ok
	}
	return fmt.Sprintf("%v — %s", err, remedy)
}

func pick(ok bool, yes, no string) string {
	if ok {
		return yes
	}
	return no
}

// spoolCheck verifies the spool directory is creatable and writable by
// creating and removing a probe file.
func spoolCheck(stateDir string) Check {
	dir := filepath.Join(stateDir, "spool")
	c := Check{Name: "spool", Fatal: true}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		c.Detail = fmt.Sprintf("cannot create %s: %v", dir, err)
		return c
	}
	probe := filepath.Join(dir, ".selfcheck")
	if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
		c.Detail = fmt.Sprintf("cannot write in %s: %v", dir, err)
		return c
	}
	os.Remove(probe)
	c.OK = true
	c.Detail = dir + " writable"
	return c
}
