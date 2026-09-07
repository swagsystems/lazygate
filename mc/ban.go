package mc

import (
	"encoding/json"
	"net"
	"os"
	"strings"
	"time"

	"lazymc/util"
)

// Ban file name.
const BanFile = "banned-ips.json"

// The forever expiry literal.
const expiryForever = "forever"

// Default ban reason if unknown.
const DefaultBanReason = "Banned by an operator."

// BannedIps is a list of banned IPs.
type BannedIps struct {
	ips map[string]BannedIp
}

// NewBannedIps returns an empty ban list.
func NewBannedIps() BannedIps {
	return BannedIps{ips: map[string]BannedIp{}}
}

// Get returns the ban entry for an IP if it exists.
func (b BannedIps) Get(ip net.IP) (BannedIp, bool) {
	e, ok := b.ips[ip.String()]
	return e, ok
}

// IsBanned reports whether the given IP is banned.
func (b BannedIps) IsBanned(ip net.IP) bool {
	e, ok := b.ips[ip.String()]
	if !ok {
		return false
	}
	return e.IsBanned()
}

// A banned IP entry, matching banned-ips.json.
type BannedIp struct {
	IP      string  `json:"ip"`
	Created *string `json:"created"`
	Source  *string `json:"source"`
	Expires *string `json:"expires"`
	Reason  *string `json:"reason"`
}

// IsBanned checks whether this entry is currently banned.
func (b BannedIp) IsBanned() bool {
	expires, ok := b.expiry()
	if !ok {
		return true
	}

	if expires != nil {
		return expires.After(time.Now())
	}

	return true
}

// expiry returns the parsed expiry time. A nil result with ok=true means
// forever. ok=false means the value couldn't be parsed (treat as banned).
func (b BannedIp) expiry() (*time.Time, bool) {
	if b.Expires == nil {
		return nil, true
	}

	if strings.ToLower(strings.TrimSpace(*b.Expires)) == expiryForever {
		return nil, true
	}

	t, err := time.ParseInLocation("2006-01-02 15:04:05 -0700", *b.Expires, time.Local)
	if err != nil {
		util.Error("lazymc", "Failed to parse ban expiry '%s', assuming still banned: %v", *b.Expires, err)
		return nil, false
	}
	return &t, true
}

// LoadBannedIps loads banned IPs from file.
func LoadBannedIps(path string) (BannedIps, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return BannedIps{}, err
	}

	var ips []BannedIp
	if err := json.Unmarshal(contents, &ips); err != nil {
		return BannedIps{}, err
	}
	util.Debug("lazymc", "Loaded %d banned IPs", len(ips))

	out := NewBannedIps()
	for _, ip := range ips {
		out.ips[ip.IP] = ip
	}
	return out, nil
}
