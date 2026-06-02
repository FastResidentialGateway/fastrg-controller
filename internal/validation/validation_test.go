package validation

import (
	"errors"
	"testing"
)

func validInput() HSIConfigInput {
	return HSIConfigInput{
		UserID:       "2",
		VlanID:       "100",
		AccountName:  "user@isp.net",
		Password:     "secret",
		DHCPAddrPool: "192.168.1.2-192.168.1.200",
		DHCPSubnet:   "255.255.255.0",
		DHCPGateway:  "192.168.1.1",
	}
}

func TestValidateHSIConfig(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*HSIConfigInput)
		wantErr string
	}{
		{name: "all fields present", mutate: func(*HSIConfigInput) {}, wantErr: ""},
		{name: "missing user id", mutate: func(in *HSIConfigInput) { in.UserID = "" }, wantErr: "User ID is required"},
		{name: "missing vlan id", mutate: func(in *HSIConfigInput) { in.VlanID = "" }, wantErr: "VLAN ID is required"},
		{name: "missing account name", mutate: func(in *HSIConfigInput) { in.AccountName = "" }, wantErr: "Account Name is required"},
		{name: "missing password", mutate: func(in *HSIConfigInput) { in.Password = "" }, wantErr: "Password is required"},
		{name: "missing dhcp pool", mutate: func(in *HSIConfigInput) { in.DHCPAddrPool = "" }, wantErr: "DHCP Address Pool is required"},
		{name: "missing dhcp subnet", mutate: func(in *HSIConfigInput) { in.DHCPSubnet = "" }, wantErr: "DHCP Subnet is required"},
		{name: "missing dhcp gateway", mutate: func(in *HSIConfigInput) { in.DHCPGateway = "" }, wantErr: "DHCP Gateway is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := validInput()
			tt.mutate(&in)
			err := ValidateHSIConfig(in)
			assertErr(t, err, tt.wantErr, false)
		})
	}
}

func TestValidateHSIConfigFieldOrder(t *testing.T) {
	// Multiple missing fields should report the first one in declared order.
	in := HSIConfigInput{}
	err := ValidateHSIConfig(in)
	assertErr(t, err, "User ID is required", false)
}

func TestValidateUserIDMatch(t *testing.T) {
	if err := ValidateUserIDMatch("2", "2"); err != nil {
		t.Fatalf("expected match, got %v", err)
	}
	assertErr(t, ValidateUserIDMatch("2", "3"), "User ID mismatch", false)
}

func TestCheckSubscriberCount(t *testing.T) {
	tests := []struct {
		name            string
		userID          string
		subscriberCount int
		wantErr         string
	}{
		{name: "no limit known", userID: "999", subscriberCount: -1, wantErr: ""},
		{name: "within limit", userID: "100", subscriberCount: 100, wantErr: ""},
		{name: "exceeds limit", userID: "101", subscriberCount: 100, wantErr: "User ID exceeds subscriber count"},
		{name: "non-numeric user id skipped", userID: "abc", subscriberCount: 1, wantErr: ""},
		{name: "zero count rejects positive id", userID: "1", subscriberCount: 0, wantErr: "User ID exceeds subscriber count"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertErr(t, CheckSubscriberCount(tt.userID, tt.subscriberCount), tt.wantErr, false)
		})
	}
}

func TestCheckVlanUnique(t *testing.T) {
	existing := []VlanOwner{
		{UserID: "1", VlanID: "100"},
		{UserID: "2", VlanID: "200"},
	}
	tests := []struct {
		name          string
		vlanID        string
		currentUserID string
		wantErr       string
		wantConflict  bool
	}{
		{name: "free vlan", vlanID: "300", currentUserID: "3", wantErr: "", wantConflict: false},
		{name: "same user keeps own vlan", vlanID: "100", currentUserID: "1", wantErr: "", wantConflict: false},
		{name: "taken by another user", vlanID: "100", currentUserID: "3", wantErr: "Input VLAN has been already used by other user: 1", wantConflict: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertErr(t, CheckVlanUnique(tt.vlanID, tt.currentUserID, existing), tt.wantErr, tt.wantConflict)
		})
	}
}

func TestValidateSubscriberCount(t *testing.T) {
	if err := ValidateSubscriberCount(0); err != nil {
		t.Fatalf("expected 0 to be valid, got %v", err)
	}
	if err := ValidateSubscriberCount(100); err != nil {
		t.Fatalf("expected 100 to be valid, got %v", err)
	}
	assertErr(t, ValidateSubscriberCount(-1), "Subscriber count must be non-negative", false)
}

func TestValidateDnsRecord(t *testing.T) {
	tests := []struct {
		name    string
		domain  string
		ip      string
		ttl     uint32
		wantErr string
	}{
		{name: "valid", domain: "a.example.com", ip: "1.2.3.4", ttl: 60, wantErr: ""},
		{name: "missing domain", domain: "", ip: "1.2.3.4", ttl: 60, wantErr: "Domain is required"},
		{name: "missing ip", domain: "a.example.com", ip: "", ttl: 60, wantErr: "IP is required"},
		{name: "zero ttl", domain: "a.example.com", ip: "1.2.3.4", ttl: 0, wantErr: "TTL must be greater than 0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertErr(t, ValidateDnsRecord(tt.domain, tt.ip, tt.ttl), tt.wantErr, false)
		})
	}
}

// assertErr checks err against an expected message ("" means no error) and,
// when an error is expected, that its Conflict flag matches.
func assertErr(t *testing.T, err error, wantMsg string, wantConflict bool) {
	t.Helper()
	if wantMsg == "" {
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		return
	}
	if err == nil {
		t.Fatalf("expected error %q, got nil", wantMsg)
	}
	if err.Error() != wantMsg {
		t.Fatalf("expected error %q, got %q", wantMsg, err.Error())
	}
	var ve *Error
	if !errors.As(err, &ve) {
		t.Fatalf("expected *validation.Error, got %T", err)
	}
	if ve.Conflict != wantConflict {
		t.Fatalf("expected Conflict=%v, got %v", wantConflict, ve.Conflict)
	}
}
