// Package sd constructs KACS binary Security Descriptors for loregd.
//
// This package builds the minimal SD structures needed for hive root
// creation at first boot. It is NOT a general-purpose SD library —
// it produces exactly the self-relative binary format defined by the
// KACS v0.20 specification for the specific SDs loregd needs.
package sd

import "encoding/binary"

// Well-known SIDs used in default hive root SDs.
var (
	// S-1-5-18 (Local System)
	SIDSystem = SID{
		Revision:            1,
		IdentifierAuthority: [6]byte{0, 0, 0, 0, 0, 5},
		SubAuthorities:      []uint32{18},
	}

	// S-1-5-32-544 (BUILTIN\Administrators)
	SIDAdministrators = SID{
		Revision:            1,
		IdentifierAuthority: [6]byte{0, 0, 0, 0, 0, 5},
		SubAuthorities:      []uint32{32, 544},
	}

	// S-1-5-11 (Authenticated Users)
	SIDAuthenticatedUsers = SID{
		Revision:            1,
		IdentifierAuthority: [6]byte{0, 0, 0, 0, 0, 5},
		SubAuthorities:      []uint32{11},
	}
)

// Registry access rights (LCS v0.21 constants).
const (
	KeyQueryValue       = 0x0001
	KeySetValue         = 0x0002
	KeyCreateSubKey     = 0x0004
	KeyEnumerateSubKeys = 0x0008
	KeyNotify           = 0x0010
	KeyCreateLink       = 0x0020

	Delete       = 0x00010000
	ReadControl  = 0x00020000
	WriteDac     = 0x00040000
	WriteOwner   = 0x00080000

	KeyAllAccess = 0x000F003F
	KeyRead      = 0x00020019
)

// SD control flags.
const (
	seSelfRelative = 0x8000
	seDaclPresent  = 0x0004
)

// ACE types.
const (
	accessAllowedACEType = 0x00
)

// ACE flags.
const (
	containerInheritACE = 0x02
)

// SID represents a Security Identifier.
type SID struct {
	Revision            uint8
	IdentifierAuthority [6]byte
	SubAuthorities      []uint32
}

// Size returns the binary size of the SID.
func (s *SID) Size() int {
	return 1 + 1 + 6 + len(s.SubAuthorities)*4
}

// Marshal appends the binary SID to dst and returns the extended slice.
func (s *SID) Marshal(dst []byte) []byte {
	dst = append(dst, s.Revision)
	dst = append(dst, byte(len(s.SubAuthorities)))
	dst = append(dst, s.IdentifierAuthority[:]...)
	for _, sa := range s.SubAuthorities {
		dst = binary.LittleEndian.AppendUint32(dst, sa)
	}
	return dst
}

// ace represents an ACCESS_ALLOWED_ACE.
type ace struct {
	Flags uint8
	Mask  uint32
	SID   *SID
}

func (a *ace) size() int {
	return 4 + 4 + a.SID.Size() // header(4) + mask(4) + SID
}

func (a *ace) marshal(dst []byte) []byte {
	aceSize := uint16(a.size())
	dst = append(dst, accessAllowedACEType)
	dst = append(dst, a.Flags)
	dst = binary.LittleEndian.AppendUint16(dst, aceSize)
	dst = binary.LittleEndian.AppendUint32(dst, a.Mask)
	dst = a.SID.Marshal(dst)
	return dst
}

// DefaultHiveRootSD returns the default Security Descriptor for a hive
// root key in self-relative binary format.
//
// The SD grants:
//   - SYSTEM: KEY_ALL_ACCESS with container-inherit
//   - Administrators: KEY_ALL_ACCESS with container-inherit
//   - Authenticated Users: KEY_READ with container-inherit
//
// Owner and group are both SYSTEM.
func DefaultHiveRootSD() []byte {
	owner := &SIDSystem
	group := &SIDSystem
	aces := []ace{
		{Flags: containerInheritACE, Mask: KeyAllAccess, SID: &SIDSystem},
		{Flags: containerInheritACE, Mask: KeyAllAccess, SID: &SIDAdministrators},
		{Flags: containerInheritACE, Mask: KeyRead, SID: &SIDAuthenticatedUsers},
	}

	// Compute sizes.
	aclHeaderSize := 8
	totalACESize := 0
	for i := range aces {
		totalACESize += aces[i].size()
	}
	aclSize := aclHeaderSize + totalACESize

	sdHeaderSize := 20
	offsetDacl := uint32(sdHeaderSize)
	offsetOwner := uint32(sdHeaderSize + aclSize)
	offsetGroup := offsetOwner + uint32(owner.Size())
	totalSize := int(offsetGroup) + group.Size()

	buf := make([]byte, 0, totalSize)

	// SD header (20 bytes).
	buf = append(buf, 1) // Revision
	buf = append(buf, 0) // Sbz1
	buf = binary.LittleEndian.AppendUint16(buf, seSelfRelative|seDaclPresent)
	buf = binary.LittleEndian.AppendUint32(buf, offsetOwner)
	buf = binary.LittleEndian.AppendUint32(buf, offsetGroup)
	buf = binary.LittleEndian.AppendUint32(buf, 0) // OffsetSacl (none)
	buf = binary.LittleEndian.AppendUint32(buf, offsetDacl)

	// DACL: ACL header (8 bytes).
	buf = append(buf, 2) // AclRevision
	buf = append(buf, 0) // Sbz1
	buf = binary.LittleEndian.AppendUint16(buf, uint16(aclSize))
	buf = binary.LittleEndian.AppendUint16(buf, uint16(len(aces)))
	buf = binary.LittleEndian.AppendUint16(buf, 0) // Sbz2

	// DACL: ACEs.
	for i := range aces {
		buf = aces[i].marshal(buf)
	}

	// Owner SID.
	buf = owner.Marshal(buf)

	// Group SID.
	buf = group.Marshal(buf)

	return buf
}
