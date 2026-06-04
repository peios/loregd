package sd

import (
	"encoding/binary"
	"testing"
)

func TestSIDSize(t *testing.T) {
	tests := []struct {
		name string
		sid  SID
		want int
	}{
		{"SYSTEM", SIDSystem, 12},           // 1+1+6+1*4
		{"Administrators", SIDAdministrators, 16}, // 1+1+6+2*4
		{"AuthenticatedUsers", SIDAuthenticatedUsers, 12}, // 1+1+6+1*4
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.sid.Size(); got != tt.want {
				t.Errorf("Size() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestSIDMarshal(t *testing.T) {
	// S-1-5-18 (SYSTEM)
	got := SIDSystem.Marshal(nil)
	if len(got) != 12 {
		t.Fatalf("SYSTEM SID length = %d, want 12", len(got))
	}
	if got[0] != 1 { // Revision
		t.Errorf("revision = %d, want 1", got[0])
	}
	if got[1] != 1 { // SubAuthorityCount
		t.Errorf("subauth count = %d, want 1", got[1])
	}
	// Authority: {0,0,0,0,0,5}
	if got[7] != 5 {
		t.Errorf("authority last byte = %d, want 5", got[7])
	}
	// SubAuthority[0] = 18 (little-endian)
	sa := binary.LittleEndian.Uint32(got[8:12])
	if sa != 18 {
		t.Errorf("subauth[0] = %d, want 18", sa)
	}

	// S-1-5-32-544 (Administrators)
	got = SIDAdministrators.Marshal(nil)
	if len(got) != 16 {
		t.Fatalf("Administrators SID length = %d, want 16", len(got))
	}
	if got[1] != 2 {
		t.Errorf("subauth count = %d, want 2", got[1])
	}
	sa0 := binary.LittleEndian.Uint32(got[8:12])
	sa1 := binary.LittleEndian.Uint32(got[12:16])
	if sa0 != 32 || sa1 != 544 {
		t.Errorf("subauths = (%d, %d), want (32, 544)", sa0, sa1)
	}
}

func TestDefaultHiveRootSD(t *testing.T) {
	sd := DefaultHiveRootSD()

	// Expected total: 20 (header) + 72 (DACL) + 12 (owner) + 12 (group) = 116
	if len(sd) != 116 {
		t.Fatalf("SD length = %d, want 116", len(sd))
	}

	// SD header.
	if sd[0] != 1 {
		t.Errorf("SD revision = %d, want 1", sd[0])
	}
	control := binary.LittleEndian.Uint16(sd[2:4])
	if control != seSelfRelative|seDaclPresent {
		t.Errorf("control = 0x%04X, want 0x%04X", control, seSelfRelative|seDaclPresent)
	}

	offsetOwner := binary.LittleEndian.Uint32(sd[4:8])
	offsetGroup := binary.LittleEndian.Uint32(sd[8:12])
	offsetSacl := binary.LittleEndian.Uint32(sd[12:16])
	offsetDacl := binary.LittleEndian.Uint32(sd[16:20])

	if offsetSacl != 0 {
		t.Errorf("SACL offset = %d, want 0", offsetSacl)
	}
	if offsetDacl != 20 {
		t.Errorf("DACL offset = %d, want 20", offsetDacl)
	}
	if offsetOwner != 92 {
		t.Errorf("owner offset = %d, want 92", offsetOwner)
	}
	if offsetGroup != 104 {
		t.Errorf("group offset = %d, want 104", offsetGroup)
	}

	// DACL header.
	dacl := sd[offsetDacl:]
	if dacl[0] != 2 { // AclRevision
		t.Errorf("ACL revision = %d, want 2", dacl[0])
	}
	aclSize := binary.LittleEndian.Uint16(dacl[2:4])
	if aclSize != 72 {
		t.Errorf("ACL size = %d, want 72", aclSize)
	}
	aceCount := binary.LittleEndian.Uint16(dacl[4:6])
	if aceCount != 3 {
		t.Errorf("ACE count = %d, want 3", aceCount)
	}

	// ACE 1: SYSTEM, KEY_ALL_ACCESS, CONTAINER_INHERIT
	ace1 := dacl[8:]
	if ace1[0] != accessAllowedACEType {
		t.Errorf("ACE1 type = %d, want %d", ace1[0], accessAllowedACEType)
	}
	if ace1[1] != containerInheritACE {
		t.Errorf("ACE1 flags = 0x%02X, want 0x%02X", ace1[1], containerInheritACE)
	}
	ace1Size := binary.LittleEndian.Uint16(ace1[2:4])
	if ace1Size != 20 {
		t.Errorf("ACE1 size = %d, want 20", ace1Size)
	}
	ace1Mask := binary.LittleEndian.Uint32(ace1[4:8])
	if ace1Mask != KeyAllAccess {
		t.Errorf("ACE1 mask = 0x%08X, want 0x%08X", ace1Mask, KeyAllAccess)
	}
	// ACE1 SID should be SYSTEM (S-1-5-18)
	if ace1[8] != 1 || ace1[9] != 1 { // revision, subauth count
		t.Errorf("ACE1 SID header = (%d, %d), want (1, 1)", ace1[8], ace1[9])
	}

	// ACE 2: Administrators, KEY_ALL_ACCESS
	ace2 := dacl[8+ace1Size:]
	ace2Mask := binary.LittleEndian.Uint32(ace2[4:8])
	if ace2Mask != KeyAllAccess {
		t.Errorf("ACE2 mask = 0x%08X, want 0x%08X", ace2Mask, KeyAllAccess)
	}
	// ACE2 SID: Administrators (S-1-5-32-544), 2 subauths
	if ace2[9] != 2 {
		t.Errorf("ACE2 SID subauth count = %d, want 2", ace2[9])
	}

	// ACE 3: Authenticated Users, KEY_READ
	ace2Size := binary.LittleEndian.Uint16(ace2[2:4])
	ace3 := dacl[8+uint32(ace1Size)+uint32(ace2Size):]
	ace3Mask := binary.LittleEndian.Uint32(ace3[4:8])
	if ace3Mask != KeyRead {
		t.Errorf("ACE3 mask = 0x%08X, want 0x%08X", ace3Mask, KeyRead)
	}
	// ACE3 SID: Authenticated Users (S-1-5-11), 1 subauth
	if ace3[9] != 1 {
		t.Errorf("ACE3 SID subauth count = %d, want 1", ace3[9])
	}
	ace3SA := binary.LittleEndian.Uint32(ace3[16:20])
	if ace3SA != 11 {
		t.Errorf("ACE3 SID subauth[0] = %d, want 11", ace3SA)
	}

	// Owner SID at offset 92: should be SYSTEM
	ownerSID := sd[offsetOwner:]
	if ownerSID[0] != 1 || ownerSID[1] != 1 {
		t.Errorf("owner SID header = (%d, %d), want (1, 1)", ownerSID[0], ownerSID[1])
	}
	ownerSA := binary.LittleEndian.Uint32(ownerSID[8:12])
	if ownerSA != 18 {
		t.Errorf("owner subauth[0] = %d, want 18 (SYSTEM)", ownerSA)
	}

	// Group SID at offset 104: should be SYSTEM
	groupSID := sd[offsetGroup:]
	if groupSID[0] != 1 || groupSID[1] != 1 {
		t.Errorf("group SID header = (%d, %d), want (1, 1)", groupSID[0], groupSID[1])
	}
	groupSA := binary.LittleEndian.Uint32(groupSID[8:12])
	if groupSA != 18 {
		t.Errorf("group subauth[0] = %d, want 18 (SYSTEM)", groupSA)
	}
}

func TestDefaultHiveRootSDDeterministic(t *testing.T) {
	sd1 := DefaultHiveRootSD()
	sd2 := DefaultHiveRootSD()
	if len(sd1) != len(sd2) {
		t.Fatal("non-deterministic length")
	}
	for i := range sd1 {
		if sd1[i] != sd2[i] {
			t.Fatalf("non-deterministic at byte %d: %d vs %d", i, sd1[i], sd2[i])
		}
	}
}
