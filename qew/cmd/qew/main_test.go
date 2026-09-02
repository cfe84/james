package main

import "testing"

func TestValidateListenAddressDevelopmentRequiresLoopback(t *testing.T) {
	tests := []struct {
		name  string
		addr  string
		valid bool
	}{
		{name: "ipv4 loopback", addr: "127.0.0.1:8077", valid: true},
		{name: "ipv6 loopback", addr: "[::1]:8077", valid: true},
		{name: "localhost", addr: "localhost:8077", valid: true},
		{name: "wildcard", addr: ":8077", valid: false},
		{name: "ipv4 wildcard", addr: "0.0.0.0:8077", valid: false},
		{name: "private address", addr: "192.168.1.10:8077", valid: false},
		{name: "hostname", addr: "qew.example.com:8077", valid: false},
		{name: "missing port", addr: "127.0.0.1", valid: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateListenAddress(tt.addr, true)
			if (err == nil) != tt.valid {
				t.Fatalf("validateListenAddress(%q, true) error = %v, valid = %v", tt.addr, err, tt.valid)
			}
		})
	}
}

func TestValidateListenAddressProductionDoesNotApplyDevelopmentRestriction(t *testing.T) {
	if err := validateListenAddress(":8077", false); err != nil {
		t.Fatalf("validateListenAddress should accept production address for password validation: %v", err)
	}
}
