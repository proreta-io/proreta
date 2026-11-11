package mail

import (
	"errors"
	"net"
	"net/netip"
	"syscall"
)

// errBlockedAddress is returned by the dialer when the server resolves to an address that is
// never used for mail.
var errBlockedAddress = errors.New("mail: address is not allowed")

// The platform admin chooses the SMTP host, so this is not the SSRF defence of user-supplied
// URLs (monitors, webhooks), and it deliberately does not reuse their blocked ranges: a mail
// relay on a private network (RFC 1918, unique local) is the normal setup and stays allowed.
// What is refused always is what no mail server lives at and what leaks credentials of the
// platform: link-local ranges (which hold the cloud metadata service), the metadata addresses
// that are not link-local, unspecified and broadcast addresses and multicast.
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("ff00::/8"),
	netip.MustParsePrefix("255.255.255.255/32"),
	// Cloud metadata services outside the link-local ranges: AWS over IPv6, Azure's wire server,
	// Alibaba Cloud, Oracle Cloud.
	netip.MustParsePrefix("fd00:ec2::254/128"),
	netip.MustParsePrefix("168.63.129.16/32"),
	netip.MustParsePrefix("100.100.100.200/32"),
	netip.MustParsePrefix("192.0.0.192/32"),
}

// BlockedAddress reports whether the SMTP client refuses to connect to ip.
func BlockedAddress(ip netip.Addr) bool {
	ip = ip.Unmap().WithZone("")
	if ip.IsUnspecified() {
		return true
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// guardDial runs for every connection attempt, after name resolution and with the address about
// to be used, so a name that resolves to a blocked address (or rebinds to one) is refused too.
func guardDial(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errBlockedAddress
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || BlockedAddress(ip) {
		return errBlockedAddress
	}
	return nil
}
