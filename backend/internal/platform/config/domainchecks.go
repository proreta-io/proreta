package config

import (
	"net/netip"
	"strings"
	"time"
)

// DomainChecks is the configuration of the status page domain verification in cmd/jobs.
type DomainChecks struct {
	// Enabled is PRORETA_STATUS_PAGE_CUSTOM_DOMAINS (default false); off, the task does nothing.
	Enabled bool
	// CNAMETarget is the host a status page's CNAME must end at (PRORETA_STATUSPAGE_CNAME_TARGET,
	// required when enabled), lowercase without a trailing dot.
	CNAMETarget string
	// Resolver pins the recursive DNS resolver, "ip" or "ip:port" (PRORETA_DNS_RESOLVER); empty
	// uses the system's.
	Resolver string
	// Timeout bounds one DNS lookup (PRORETA_DNS_TIMEOUT, default 3s, 500ms to 10s).
	Timeout time.Duration
}

func (l *loader) domainChecks() DomainChecks {
	c := DomainChecks{
		Enabled: l.boolean("PRORETA_STATUS_PAGE_CUSTOM_DOMAINS", false),
		Timeout: l.duration("PRORETA_DNS_TIMEOUT", 3*time.Second, 500*time.Millisecond, 10*time.Second),
	}
	if r := l.optional("PRORETA_DNS_RESOLVER"); r != "" {
		if _, err := netip.ParseAddrPort(r); err != nil {
			if _, err := netip.ParseAddr(strings.Trim(r, "[]")); err != nil {
				l.fail("PRORETA_DNS_RESOLVER", "must be an IP address, optionally with a port, such as 10.0.0.2 or [::1]:53")
			}
		}
		c.Resolver = r
	}
	if !c.Enabled {
		return c
	}
	target := strings.TrimSuffix(strings.ToLower(l.required("PRORETA_STATUSPAGE_CNAME_TARGET")), ".")
	labels := strings.Split(target, ".")
	ok := len(labels) >= 2
	for _, label := range labels {
		ok = ok && isDNSLabel(label)
	}
	if target != "" && !ok {
		l.fail("PRORETA_STATUSPAGE_CNAME_TARGET", "must be a host name such as status.example.net")
	}
	c.CNAMETarget = target
	return c
}
