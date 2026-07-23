package server

import (
	"errors"
	"reflect"
	"testing"

	controllerpb "fastrg-controller/proto"
)

func TestMergeRESTHSIConfigUpdateToggles(t *testing.T) {
	tests := []struct {
		name       string
		requestDNS *bool
		requestTCP *bool
		current    *HSIConfig
		wantDNS    bool
		wantTCP    bool
	}{
		{
			name:    "omitted inherits current true and false",
			current: &HSIConfig{DNSProxyEnable: boolPointer(true), TCPConntrackEnable: boolPointer(false)},
			wantDNS: true,
			wantTCP: false,
		},
		{
			name:    "omitted inherits current false and true",
			current: &HSIConfig{DNSProxyEnable: boolPointer(false), TCPConntrackEnable: boolPointer(true)},
			wantDNS: false,
			wantTCP: true,
		},
		{
			name:    "omitted defaults true when current fields are missing",
			current: &HSIConfig{},
			wantDNS: true,
			wantTCP: true,
		},
		{
			name:    "omitted defaults true when current does not exist",
			current: nil,
			wantDNS: true,
			wantTCP: true,
		},
		{
			name:       "explicit false replaces current true",
			requestDNS: boolPointer(false),
			requestTCP: boolPointer(false),
			current:    &HSIConfig{DNSProxyEnable: boolPointer(true), TCPConntrackEnable: boolPointer(true)},
			wantDNS:    false,
			wantTCP:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requested := HSIConfig{
				UserID:             "7",
				DNSProxyEnable:     tt.requestDNS,
				TCPConntrackEnable: tt.requestTCP,
			}
			got := mergeRESTHSIConfigUpdate(requested, tt.current)
			if got.DNSProxyEnable == nil || *got.DNSProxyEnable != tt.wantDNS {
				t.Fatalf("DNSProxyEnable = %v, want %v", got.DNSProxyEnable, tt.wantDNS)
			}
			if got.TCPConntrackEnable == nil || *got.TCPConntrackEnable != tt.wantTCP {
				t.Fatalf("TCPConntrackEnable = %v, want %v", got.TCPConntrackEnable, tt.wantTCP)
			}
		})
	}
}

func TestMergeRESTHSIConfigUpdatePortMappings(t *testing.T) {
	oldMappings := []PortMapping{{Index: "old", DIP: "192.0.2.10"}}
	newMappings := []PortMapping{{Index: "new", DIP: "192.0.2.20"}}
	tests := []struct {
		name      string
		requested []PortMapping
		current   *HSIConfig
		want      []PortMapping
	}{
		{
			name:      "omitted inherits current mappings",
			requested: nil,
			current:   &HSIConfig{PortMappings: oldMappings},
			want:      oldMappings,
		},
		{
			name:      "omitted without current produces empty mappings",
			requested: nil,
			current:   nil,
			want:      []PortMapping{},
		},
		{
			name:      "explicit empty clears mappings",
			requested: []PortMapping{},
			current:   &HSIConfig{PortMappings: oldMappings},
			want:      []PortMapping{},
		},
		{
			name:      "non-empty replaces mappings",
			requested: newMappings,
			current:   &HSIConfig{PortMappings: oldMappings},
			want:      newMappings,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeRESTHSIConfigUpdate(HSIConfig{PortMappings: tt.requested}, tt.current)
			if !reflect.DeepEqual(got.PortMappings, tt.want) {
				t.Fatalf("PortMappings = %+v, want %+v", got.PortMappings, tt.want)
			}
		})
	}
}

func TestMergeRESTHSIConfigUpdateIgnoresRequestedDesireStatus(t *testing.T) {
	tests := []struct {
		name      string
		requested string
		current   *HSIConfig
		want      string
	}{
		{name: "connect body ignored", requested: desireStatusConnect, current: &HSIConfig{DesireStatus: desireStatusDisconnect}, want: desireStatusDisconnect},
		{name: "disconnect body ignored", requested: desireStatusDisconnect, current: &HSIConfig{DesireStatus: desireStatusConnect}, want: desireStatusConnect},
		{name: "arbitrary body ignored", requested: "unexpected", current: &HSIConfig{DesireStatus: desireStatusConnect}, want: desireStatusConnect},
		{name: "missing current defaults disconnect", requested: desireStatusConnect, current: nil, want: desireStatusDisconnect},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeRESTHSIConfigUpdate(HSIConfig{DesireStatus: tt.requested}, tt.current)
			if got.DesireStatus != tt.want {
				t.Fatalf("DesireStatus = %q, want %q", got.DesireStatus, tt.want)
			}
		})
	}
}

func TestProtoToInnerOptionalTogglePresence(t *testing.T) {
	tests := []struct {
		name    string
		value   *bool
		wantNil bool
		want    bool
	}{
		{name: "unset remains nil", value: nil, wantNil: true},
		{name: "explicit false remains present", value: boolPointer(false), want: false},
		{name: "explicit true remains present", value: boolPointer(true), want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := protoToInner(&controllerpb.HSIConfig{
				DnsProxyEnable:     tt.value,
				TcpConntrackEnable: tt.value,
			})
			if tt.wantNil {
				if got.DNSProxyEnable != nil || got.TCPConntrackEnable != nil {
					t.Fatalf("optional toggles = (%v, %v), want both nil", got.DNSProxyEnable, got.TCPConntrackEnable)
				}
				return
			}
			if got.DNSProxyEnable == nil || *got.DNSProxyEnable != tt.want {
				t.Fatalf("DNSProxyEnable = %v, want present %v", got.DNSProxyEnable, tt.want)
			}
			if got.TCPConntrackEnable == nil || *got.TCPConntrackEnable != tt.want {
				t.Fatalf("TCPConntrackEnable = %v, want present %v", got.TCPConntrackEnable, tt.want)
			}
		})
	}
}

func TestMergeGRPCHSIConfigUpdate(t *testing.T) {
	oldMappings := []portMapping{{Index: "old", DIP: "192.0.2.10"}}
	newMappings := []portMapping{{Index: "new", DIP: "192.0.2.20"}}
	current := &hsiConfigInner{
		DNSProxyEnable:     boolPointer(false),
		TCPConntrackEnable: boolPointer(false),
		PortMappings:       oldMappings,
		DesireStatus:       desireStatusConnect,
	}
	tests := []struct {
		name              string
		requested         hsiConfigInner
		current           *hsiConfigInner
		clearPortMappings bool
		wantDNS           bool
		wantTCP           bool
		wantMappings      []portMapping
	}{
		{
			name:         "omitted fields inherit current",
			requested:    hsiConfigInner{DesireStatus: desireStatusDisconnect},
			current:      current,
			wantDNS:      false,
			wantTCP:      false,
			wantMappings: oldMappings,
		},
		{
			name: "explicit false replaces current true",
			requested: hsiConfigInner{
				DNSProxyEnable:     boolPointer(false),
				TCPConntrackEnable: boolPointer(false),
			},
			current: &hsiConfigInner{
				DNSProxyEnable:     boolPointer(true),
				TCPConntrackEnable: boolPointer(true),
				PortMappings:       oldMappings,
				DesireStatus:       desireStatusConnect,
			},
			wantDNS:      false,
			wantTCP:      false,
			wantMappings: oldMappings,
		},
		{
			name:              "clear flag clears mappings",
			requested:         hsiConfigInner{},
			current:           current,
			clearPortMappings: true,
			wantDNS:           false,
			wantTCP:           false,
			wantMappings:      []portMapping{},
		},
		{
			name:         "non-empty mappings replace",
			requested:    hsiConfigInner{PortMappings: newMappings},
			current:      current,
			wantDNS:      false,
			wantTCP:      false,
			wantMappings: newMappings,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeGRPCHSIConfigUpdate(tt.requested, tt.current, tt.clearPortMappings)
			if got.DNSProxyEnable == nil || *got.DNSProxyEnable != tt.wantDNS {
				t.Fatalf("DNSProxyEnable = %v, want %v", got.DNSProxyEnable, tt.wantDNS)
			}
			if got.TCPConntrackEnable == nil || *got.TCPConntrackEnable != tt.wantTCP {
				t.Fatalf("TCPConntrackEnable = %v, want %v", got.TCPConntrackEnable, tt.wantTCP)
			}
			if !reflect.DeepEqual(got.PortMappings, tt.wantMappings) {
				t.Fatalf("PortMappings = %+v, want %+v", got.PortMappings, tt.wantMappings)
			}
			if got.DesireStatus != desireStatusConnect {
				t.Fatalf("DesireStatus = %q, want current %q", got.DesireStatus, desireStatusConnect)
			}
		})
	}
}

func TestMergeGRPCHSIConfigUpdateDefaultsWithoutCurrent(t *testing.T) {
	got := mergeGRPCHSIConfigUpdate(hsiConfigInner{}, nil, false)
	if got.DNSProxyEnable == nil || !*got.DNSProxyEnable {
		t.Fatalf("DNSProxyEnable = %v, want default true", got.DNSProxyEnable)
	}
	if got.TCPConntrackEnable == nil || !*got.TCPConntrackEnable {
		t.Fatalf("TCPConntrackEnable = %v, want default true", got.TCPConntrackEnable)
	}
	if len(got.PortMappings) != 0 {
		t.Fatalf("PortMappings = %+v, want empty", got.PortMappings)
	}
	if got.DesireStatus != desireStatusDisconnect {
		t.Fatalf("DesireStatus = %q, want %q", got.DesireStatus, desireStatusDisconnect)
	}
}

func TestValidateGRPCPortMappingUpdate(t *testing.T) {
	tests := []struct {
		name      string
		mappings  []portMapping
		clear     bool
		wantError bool
	}{
		{name: "empty and preserve", mappings: nil, clear: false},
		{name: "empty and clear", mappings: nil, clear: true},
		{name: "non-empty replacement", mappings: []portMapping{{Index: "0"}}, clear: false},
		{name: "non-empty and clear conflict", mappings: []portMapping{{Index: "0"}}, clear: true, wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateGRPCPortMappingUpdate(hsiConfigInner{PortMappings: tt.mappings}, tt.clear)
			if tt.wantError && !errors.Is(err, errConflictingPortMappingUpdate) {
				t.Fatalf("error = %v, want %v", err, errConflictingPortMappingUpdate)
			}
			if !tt.wantError && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
