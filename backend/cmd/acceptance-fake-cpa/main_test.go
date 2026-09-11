package main

import "testing"

func TestValidateLoopbackListenAddress(t *testing.T) {
	for _, test := range []struct {
		address string
		wantErr bool
	}{
		{address: "127.0.0.1:18319"},
		{address: "localhost:18319"},
		{address: "[::1]:18319"},
		{address: ":18319", wantErr: true},
		{address: "0.0.0.0:18319", wantErr: true},
		{address: "192.168.1.10:18319", wantErr: true},
		{address: "127.0.0.1", wantErr: true},
	} {
		t.Run(test.address, func(t *testing.T) {
			err := validateLoopbackListenAddress(test.address)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateLoopbackListenAddress(%q) error = %v, wantErr %t", test.address, err, test.wantErr)
			}
		})
	}
}
