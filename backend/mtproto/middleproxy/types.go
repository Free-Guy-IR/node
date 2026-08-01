// Package middleproxy implements Telegram's MTProto "middle proxy" relay
// protocol - the transport used by mtg v1 (and the reference C
// implementation) to support ad-tag/sponsor-channel promotion. mtg v2 (and
// this repo's forked mtglib dependency) removed this transport entirely in
// favor of relaying straight to Telegram DCs, which is simpler and lower
// overhead but cannot carry an ad tag. This package is a from-scratch,
// adapted port of the relevant pieces of 9seconds/mtg v1
// (github.com/9seconds/mtg, tag v1.0.12): obfuscated2 framing, the
// middle-proxy RPC handshake, and per-packet ad-tag-carrying relay.
//
// Unlike v1 (one secret, one OS process), this package supports many users
// sharing one instance, matching this repo's mtglib-based path: each
// connection's secret is matched against a caller-supplied candidate set at
// handshake time.
//
// Unlike v1, this package does not pool/multiplex physical connections to
// middle-proxy servers (v1's hub/mux/connection machinery) - each client
// connection gets its own physical middle-proxy connection. Telegram's
// middle-proxy servers do not require multiplexing; it was purely a
// connection-count optimization in v1. Given ad-tag traffic is expected to
// be a minority of total MTProto volume, the simpler 1:1 model was chosen
// to keep this security-sensitive port reviewable.
package middleproxy

// DC is a Telegram datacenter index, as carried inside the client's
// obfuscated2 handshake frame.
type DC int16

// DefaultDC is used when a client's handshake frame does not carry a
// parseable DC index.
const DefaultDC DC = 2

// ConnectionType is the wire framing a client chose in its obfuscated2
// handshake frame (encoded as the frame's Magic() field).
type ConnectionType uint8

const (
	ConnectionTypeUnknown ConnectionType = iota
	ConnectionTypeAbridged
	ConnectionTypeIntermediate
	ConnectionTypeSecure
)

var (
	connectionTagAbridged     = []byte{0xef, 0xef, 0xef, 0xef}
	connectionTagIntermediate = []byte{0xee, 0xee, 0xee, 0xee}
	connectionTagSecure       = []byte{0xdd, 0xdd, 0xdd, 0xdd}
)

// ConnectionProtocol says whether a dialed address is IPv4 or IPv6 - it
// drives which of the two DC address tables (fetched from
// core.telegram.org) a connection's middle-proxy dial uses.
type ConnectionProtocol uint8

const (
	ConnectionProtocolIPv4 ConnectionProtocol = 1
	ConnectionProtocolIPv6 ConnectionProtocol = 2
)

// Packet is one discrete MTProto message, already stripped of whichever
// client-chosen wire framing (abridged/intermediate/secure) carried it.
type Packet []byte
