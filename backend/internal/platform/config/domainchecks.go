package config

import (
	"net/netip"
	"strings"
	"time"
)

// DNS is how api and jobs ask DNS about status page domains.
type DNS struct {
	// Resolver pins the recursive DNS resolver, "ip" or "ip:port" (PRORETA_DNS_RESOLVER); empty
	// uses the system's.
	Resolver string
	// Timeout bounds one DNS lookup (PRORETA_DNS_TIMEOUT, default 3s, 500ms to 10s).
	Timeout time.Duration
}

// DomainChecks is the configuration of the status page domain verification in cmd/jobs.
type DomainChecks struct {
	// Enabled is PRORETA_STATUS_PAGE_CUSTOM_DOMAINS (default false); off, the task does nothing.
	Enabled bool
	// CNAMETarget is PRORETA_STATUSPAGE_CNAME_TARGET, validated as in the api; empty means domains
	// are verified but never become active.
	CNAMETarget string
	DNS
}

func (l *loader) dns() DNS {
	d := DNS{Timeout: l.duration("PRORETA_DNS_TIMEOUT", 3*time.Second, 500*time.Millisecond, 10*time.Second)}
	if r := l.optional("PRORETA_DNS_RESOLVER"); r != "" {
		if _, err := netip.ParseAddrPort(r); err != nil {
			if _, err := netip.ParseAddr(strings.Trim(r, "[]")); err != nil {
				l.fail("PRORETA_DNS_RESOLVER", "must be an IP address, optionally with a port, such as 10.0.0.2 or [::1]:53")
			}
		}
		d.Resolver = r
	}
	return d
}

func (l *loader) domainChecks() DomainChecks {
	c := DomainChecks{Enabled: l.boolean("PRORETA_STATUS_PAGE_CUSTOM_DOMAINS", false), DNS: l.dns()}
	c.CNAMETarget = l.cnameTarget()
	return c
}
