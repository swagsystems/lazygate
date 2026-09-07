// Package proto implements the Minecraft protocol primitives lazymc uses.
package proto

// Default minecraft protocol version name.
//
// Just something to default to when real server version isn't known or when
// no hint is specified in the configuration.
const ProtoDefaultVersion = "1.20.3"

// Default minecraft protocol version.
const ProtoDefaultProtocol = 765

// Compression threshold to use.
const CompressionThreshold = 256

// Default buffer size when reading packets.
const BufSize = 8 * 1024
