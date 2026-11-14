package api

import "errors"

// errCustomDomainsDisabled answers every status page domain operation while
// PRORETA_STATUS_PAGE_CUSTOM_DOMAINS is off.
var errCustomDomainsDisabled = errors.New("custom domains disabled")
