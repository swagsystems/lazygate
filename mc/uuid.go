package mc

import (
	"crypto/md5"
	"encoding/binary"
)

// Offline player namespace.
const offlinePlayerNamespace = "OfflinePlayer:"

// OfflinePlayerUUID computes the offline-mode UUID for a username, matching
// Java's `UUID.nameUUIDFromBytes("OfflinePlayer:" + name)`.
//
// The result is a version 3 (name based, MD5) UUID.
func OfflinePlayerUUID(username string) [16]byte {
	return javaNameUUIDFromBytes([]byte(offlinePlayerNamespace + username))
}

// javaNameUUIDFromBytes mirrors Java's UUID.nameUUIDFromBytes.
func javaNameUUIDFromBytes(data []byte) [16]byte {
	sum := md5.Sum(data)

	// Clear version, set to version 3
	sum[6] &= 0x0f
	sum[6] |= 0x30

	// Clear variant, set to IETF variant
	sum[8] &= 0x3f
	sum[8] |= 0x80

	var out [16]byte
	copy(out[:], sum[:])
	return out
}

// UuidVersion returns the version nibble of a UUID.
func UuidVersion(u [16]byte) byte {
	return u[6] >> 4
}

// UUIDVersionMd5 is the version value for MD5 (name based) UUIDs.
const UUIDVersionMd5 = 3

var _ = binary.BigEndian // keep import balanced if unused later
