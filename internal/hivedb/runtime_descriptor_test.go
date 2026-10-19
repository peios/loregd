package hivedb

import (
	"encoding/hex"
	"testing"

	"github.com/peios/peios-go/security"
)

func TestRuntimeDescriptorRetainsProtectedSystemOnlyPolicy(t *testing.T) {
	descriptor, err := security.ParseSDDL(runtimeDescriptorSDDL)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := descriptor.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	// Captured from libp-go v0.9.0 before its replacement. This checks the
	// owner, group, protected DACL and inheritable SYSTEM-only ACE.
	const expected = "010004901400000020000000000000002c000000010100000000000512000000" +
		"01010000000000051200000002001c00010000000003140000000010010100000000000512000000"
	if actual := hex.EncodeToString(encoded); actual != expected {
		t.Fatalf("runtime descriptor = %s, want %s", actual, expected)
	}
}
