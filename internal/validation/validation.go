// Package validation holds transport-neutral validation rules for FastRG
// configuration objects (HSI config, DNS records, subscriber count).
//
// The rules are pure functions: they operate only on values passed in by the
// caller and never touch etcd or any transport. This keeps them unit-testable
// and lets every write path share the exact same rules — the REST API today,
// and the CLI-facing gRPC config service in a later slice. Any I/O (reading
// existing configs from etcd to compute VLAN uniqueness, reading the
// subscriber count) stays in the caller; only the decision lives here.
package validation

import (
	"fmt"
	"regexp"
	"strconv"
)

var (
	nodeIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	userIDPattern = regexp.MustCompile(`^[0-9]+$`)
)

// Error is a validation failure. Conflict distinguishes a uniqueness clash
// (HTTP 409) from an ordinary bad-input error (HTTP 400) so callers can map it
// to the right status without string matching.
type Error struct {
	Msg      string
	Conflict bool
}

func (e *Error) Error() string { return e.Msg }

func badRequest(format string, args ...interface{}) *Error {
	return &Error{Msg: fmt.Sprintf(format, args...)}
}

// ValidateNodeID checks that a node identifier is safe to embed in an etcd
// key while retaining the UUID and human-readable names used by FastRG.
func ValidateNodeID(id string) error {
	if id == "" {
		return badRequest("Node ID is required")
	}
	if !nodeIDPattern.MatchString(id) {
		return badRequest("Node ID must be 1-64 characters containing only letters, numbers, underscores, or hyphens")
	}
	return nil
}

// ValidateUserID checks that a user identifier has one canonical decimal
// representation and fits in the uint32 range shared by the API contract.
func ValidateUserID(id string) error {
	if id == "" {
		return badRequest("User ID is required")
	}
	if !userIDPattern.MatchString(id) {
		return badRequest("User ID must contain only digits")
	}
	if id == "0" {
		return badRequest("User ID must be greater than 0")
	}
	if id[0] == '0' {
		return badRequest("User ID must not contain leading zeros")
	}
	if _, err := strconv.ParseUint(id, 10, 32); err != nil {
		return badRequest("User ID must fit in uint32")
	}
	return nil
}

// HSIConfigInput is the transport-neutral view of an HSI config used for field
// validation. The REST handler maps its HSIConfig into this; the gRPC handler
// will map its proto message into the same struct.
type HSIConfigInput struct {
	UserID       string
	VlanID       string
	AccountName  string
	Password     string
	DHCPAddrPool string
	DHCPSubnet   string
	DHCPGateway  string
}

// VlanOwner pairs a user with the VLAN it currently occupies on a node. The
// caller builds this slice from the node's existing configs in etcd.
type VlanOwner struct {
	UserID string
	VlanID string
}

// ValidateHSIConfig checks that all required HSI fields are present. It returns
// the first missing field, in the same order the REST handler used to check.
func ValidateHSIConfig(in HSIConfigInput) error {
	switch {
	case in.UserID == "":
		return badRequest("User ID is required")
	case in.VlanID == "":
		return badRequest("VLAN ID is required")
	case in.AccountName == "":
		return badRequest("Account Name is required")
	case in.Password == "":
		return badRequest("Password is required")
	case in.DHCPAddrPool == "":
		return badRequest("DHCP Address Pool is required")
	case in.DHCPSubnet == "":
		return badRequest("DHCP Subnet is required")
	case in.DHCPGateway == "":
		return badRequest("DHCP Gateway is required")
	}
	return nil
}

// ValidateUserIDMatch ensures the user id in the URL path matches the one in
// the request body.
func ValidateUserIDMatch(urlUserID, bodyUserID string) error {
	if urlUserID != bodyUserID {
		return badRequest("User ID mismatch")
	}
	return nil
}

// CheckSubscriberCount enforces that a numeric user id does not exceed the
// node's subscriber count. A negative subscriberCount means "no limit known"
// (the caller could not read a valid count) and skips the check; a non-numeric
// user id is also left alone, matching the previous behaviour.
func CheckSubscriberCount(userID string, subscriberCount int) error {
	if subscriberCount < 0 {
		return nil
	}
	uidNum, err := strconv.Atoi(userID)
	if err != nil {
		return nil
	}
	if uidNum > subscriberCount {
		return badRequest("User ID exceeds subscriber count")
	}
	return nil
}

// CheckVlanUnique reports a conflict if vlanID is already taken by a different
// user among the node's existing configs.
func CheckVlanUnique(vlanID, currentUserID string, existing []VlanOwner) error {
	for _, owner := range existing {
		if owner.VlanID == vlanID && owner.UserID != currentUserID {
			return &Error{
				Msg:      fmt.Sprintf("Input VLAN has been already used by other user: %s", owner.UserID),
				Conflict: true,
			}
		}
	}
	return nil
}

// ValidateSubscriberCount enforces a non-negative subscriber count.
func ValidateSubscriberCount(count int) error {
	if count < 0 {
		return badRequest("Subscriber count must be non-negative")
	}
	return nil
}

// ValidateDnsRecord checks a static DNS record's required fields.
func ValidateDnsRecord(domain, ip string, ttl uint32) error {
	switch {
	case domain == "":
		return badRequest("Domain is required")
	case ip == "":
		return badRequest("IP is required")
	case ttl == 0:
		return badRequest("TTL must be greater than 0")
	}
	return nil
}
