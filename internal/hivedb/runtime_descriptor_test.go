package hivedb

import (
	"encoding/hex"
	"testing"

	"github.com/peios/peios-go/security"
)

func TestStorageDescriptorMatchesPackagePolicy(t *testing.T) {
	descriptor, err := security.ParseSDDL(storageDirectorySDDL)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := descriptor.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	// The recipe's sd_overrides: SYSTEM ownership, a protected DACL, and
	// inheritable full control for SYSTEM and Administrators.
	const expected = "010004901400000020000000000000002c000000010100000000000512000000" +
		"01010000000000051200000002003400020000000003140000000010010100000000000512000000" +
		"000318000000001001020000000000052000000020020000"
	if actual := hex.EncodeToString(encoded); actual != expected {
		t.Fatalf("storage descriptor = %s, want %s", actual, expected)
	}
}
