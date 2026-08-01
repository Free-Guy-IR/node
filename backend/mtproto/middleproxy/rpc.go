package middleproxy

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Sequence numbers with special meaning to the middle-proxy RPC transport:
// the nonce exchange and the handshake each run on their own independent
// frame sequence, both seeded at these values (see rpc_frame.go's
// rpcFrameConn).
const (
	seqNoNonce     = -2
	seqNoHandshake = -1
)

var (
	tagCloseExt     = []byte{0xa2, 0x34, 0xb6, 0x5e}
	tagProxyAns     = []byte{0x0d, 0xda, 0x03, 0x44}
	tagSimpleAck    = []byte{0x9b, 0x40, 0xac, 0x3b}
	tagHandshake    = []byte{0xf5, 0xee, 0x82, 0x76}
	tagNonce        = []byte{0xaa, 0x87, 0xcb, 0x7a}
	tagProxyRequest = []byte{0xee, 0xf1, 0xce, 0x36}

	nonceCryptoAES = []byte{0x01, 0x00, 0x00, 0x00}
	handshakeFlags = []byte{0x00, 0x00, 0x00, 0x00}

	proxyRequestExtraSize = []byte{0x18, 0x00, 0x00, 0x00}
	proxyRequestProxyTag  = []byte{0xae, 0x26, 0x1e, 0xdb}

	// Fixed 12-byte "process ID" both mtg v1 and the reference C proxy send
	// for both the sender and peer PID fields of the handshake request;
	// kept identical here rather than invented, since it's the
	// known-working value middle-proxy servers accept.
	handshakePID = []byte("IPIPPRPDTIME")
)

type proxyRequestFlags uint32

const (
	proxyRequestFlagsHasAdTag     proxyRequestFlags = 0x8
	proxyRequestFlagsEncrypted    proxyRequestFlags = 0x2
	proxyRequestFlagsMagic        proxyRequestFlags = 0x1000
	proxyRequestFlagsExtMode2     proxyRequestFlags = 0x20000
	proxyRequestFlagsIntermediate proxyRequestFlags = 0x20000000
	proxyRequestFlagsAbridged     proxyRequestFlags = 0x40000000
	proxyRequestFlagsQuickAck     proxyRequestFlags = 0x80000000
	proxyRequestFlagsPad          proxyRequestFlags = 0x8000000
)

func (r proxyRequestFlags) bytes() []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, uint32(r))
	return b
}

// nonceRequest is the first, unencrypted message sent to a middle-proxy
// server: it identifies which of the server's rotating AES secrets
// (keySelector = first 4 bytes of the secret fetched from
// core.telegram.org/getProxySecret) the client wants to use, plus a random
// nonce that both sides mix into the transport key derivation.
type nonceRequest struct {
	keySelector []byte
	cryptoTS    []byte
	nonce       []byte
}

func (r *nonceRequest) bytes() []byte {
	buf := &bytes.Buffer{}
	buf.Write(tagNonce)
	buf.Write(r.keySelector)
	buf.Write(nonceCryptoAES)
	buf.Write(r.cryptoTS)
	buf.Write(r.nonce)
	return buf.Bytes()
}

func newNonceRequest(proxySecret []byte) (*nonceRequest, error) {
	nonce := make([]byte, 16)
	keySelector := make([]byte, 4)
	cryptoTS := make([]byte, 4)

	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("middleproxy: cannot generate nonce: %w", err)
	}

	copy(keySelector, proxySecret)
	timestamp := time.Now().Truncate(time.Second).Unix() % 4294967296
	binary.LittleEndian.PutUint32(cryptoTS, uint32(timestamp))

	return &nonceRequest{keySelector: keySelector, cryptoTS: cryptoTS, nonce: nonce}, nil
}

type nonceResponse struct {
	keySelector []byte
	cryptoTS    []byte
	nonce       []byte
	typ         []byte
	crypto      []byte
}

func (r *nonceResponse) valid(req *nonceRequest) error {
	if !bytes.Equal(r.typ, tagNonce) {
		return errors.New("middleproxy: unexpected nonce response tag")
	}
	if !bytes.Equal(r.crypto, nonceCryptoAES) {
		return errors.New("middleproxy: unexpected nonce response crypto type")
	}
	if !bytes.Equal(r.keySelector, req.keySelector) {
		return errors.New("middleproxy: unexpected nonce response key selector")
	}
	return nil
}

func parseNonceResponse(data []byte) (*nonceResponse, error) {
	if len(data) != 32 {
		return nil, fmt.Errorf("middleproxy: unexpected nonce response length %d", len(data))
	}
	return &nonceResponse{
		typ:         data[:4],
		keySelector: data[4:8],
		crypto:      data[8:12],
		cryptoTS:    data[12:16],
		nonce:       data[16:],
	}, nil
}

func handshakeRequestBytes() []byte {
	buf := &bytes.Buffer{}
	buf.Write(tagHandshake)
	buf.Write(handshakeFlags)
	buf.Write(handshakePID)
	buf.Write(handshakePID)
	return buf.Bytes()
}

func validHandshakeResponse(data []byte) error {
	if len(data) != 32 {
		return fmt.Errorf("middleproxy: unexpected handshake response length %d", len(data))
	}
	if !bytes.Equal(data[:4], tagHandshake) {
		return errors.New("middleproxy: unexpected handshake response tag")
	}
	// data[20:32] is the peer's echo of our sender PID - the reference
	// implementation only checks it equals what we sent.
	if !bytes.Equal(data[20:32], handshakePID) {
		return errors.New("middleproxy: unexpected handshake response peer pid")
	}
	return nil
}

type proxyResponseType uint8

const (
	proxyResponseTypeAns proxyResponseType = iota
	proxyResponseTypeSimpleAck
	proxyResponseTypeCloseExt
)

type proxyResponse struct {
	typ     proxyResponseType
	connID  [8]byte
	payload []byte
}

func parseProxyResponse(packet []byte) (*proxyResponse, error) {
	if len(packet) < 4 {
		return nil, fmt.Errorf("middleproxy: proxy response too short: %d bytes", len(packet))
	}

	tag := packet[:4]

	switch {
	case bytes.Equal(tag, tagProxyAns):
		var r proxyResponse
		if len(packet) < 16 {
			return nil, errors.New("middleproxy: proxy-ans response too short")
		}
		r.typ = proxyResponseTypeAns
		copy(r.connID[:], packet[8:16])
		r.payload = packet[16:]
		return &r, nil
	case bytes.Equal(tag, tagSimpleAck):
		var r proxyResponse
		if len(packet) < 12 {
			return nil, errors.New("middleproxy: simple-ack response too short")
		}
		r.typ = proxyResponseTypeSimpleAck
		copy(r.connID[:], packet[4:12])
		r.payload = packet[12:]
		return &r, nil
	case bytes.Equal(tag, tagCloseExt):
		return &proxyResponse{typ: proxyResponseTypeCloseExt}, nil
	}

	return nil, fmt.Errorf("middleproxy: unknown proxy response tag %x", tag)
}

func flagsString(f proxyRequestFlags) string {
	var parts []string
	if f&proxyRequestFlagsHasAdTag != 0 {
		parts = append(parts, "HAS_AD_TAG")
	}
	if f&proxyRequestFlagsEncrypted != 0 {
		parts = append(parts, "ENCRYPTED")
	}
	return strings.Join(parts, "|")
}
