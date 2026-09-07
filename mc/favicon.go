package mc

import (
	"bytes"
	_ "embed"
	"encoding/base64"
)

// Protocol version since when favicons are supported.
const faviconProtocolVersion = 4

// Default favicon bytes, embedded from lazymc's resources.
//
//go:embed res/unknown_server_optimized.png
var defaultFaviconPNG []byte

// DefaultFavicon returns the default server status favicon string.
func DefaultFavicon() string {
	return EncodeFavicon(defaultFaviconPNG)
}

// EncodeFavicon encodes favicon bytes to a string Minecraft can read.
func EncodeFavicon(data []byte) string {
	var sb bytes.Buffer
	sb.WriteString("data:image/png;base64,")
	sb.WriteString(base64.StdEncoding.EncodeToString(data))
	return sb.String()
}

// SupportsFavicon reports whether the status response favicon is supported
// based on the given client info. Defaults to true if unsure.
func SupportsFavicon(protocol *uint32) bool {
	if protocol == nil {
		return true
	}
	return *protocol >= faviconProtocolVersion
}
