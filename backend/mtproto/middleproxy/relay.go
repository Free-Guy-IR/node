package middleproxy

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"time"
)

const (
	middleProxyDialTimeout = 10 * time.Second
	relayIdleTimeout       = 3 * time.Minute
)

var encryptedPrefix [8]byte // zero prefix: MTProto's own "unencrypted message" marker.

// cbcConn wraps a raw net.Conn with the AES-CBC cipher derived by
// middleProxyCipher, presenting a plain net.Conn to rpcFrameConn. Writes
// must already be block-aligned (rpcFrameConn.Write pads every frame to
// aes.BlockSize before writing, so this always holds in practice). Reads
// decrypt one 16-byte block at a time and buffer any leftover for the next
// call, since rpcFrameConn.Read issues small, exact-length reads that don't
// line up with block boundaries.
type cbcConn struct {
	net.Conn
	enc     cipher.BlockMode
	dec     cipher.BlockMode
	readBuf []byte
}

func (c *cbcConn) Read(p []byte) (int, error) {
	for len(c.readBuf) == 0 {
		block := make([]byte, aes.BlockSize)
		if _, err := io.ReadFull(c.Conn, block); err != nil {
			return 0, err
		}
		c.dec.CryptBlocks(block, block)
		c.readBuf = block
	}

	n := copy(p, c.readBuf)
	c.readBuf = c.readBuf[n:]

	return n, nil
}

func (c *cbcConn) Write(p []byte) (int, error) {
	if len(p)%aes.BlockSize != 0 {
		return 0, fmt.Errorf("middleproxy: cbc write not block-aligned: %d bytes", len(p))
	}

	out := make([]byte, len(p))
	c.enc.CryptBlocks(out, p)

	if _, err := c.Conn.Write(out); err != nil {
		return 0, err //nolint:wrapcheck
	}

	return len(p), nil
}

// middleProxyConn is a physical, handshaken connection to one Telegram
// middle-proxy server, ready to carry proxyRequest/proxyResponse-framed
// traffic for exactly one client connection (see the package doc comment
// for why this port doesn't multiplex several clients per physical
// connection the way mtg v1's hub does).
type middleProxyConn struct {
	raw   net.Conn
	frame *rpcFrameConn
}

func (m *middleProxyConn) Close() error {
	return m.raw.Close()
}

// dialMiddleProxy establishes a physical connection to a middle-proxy
// server for dc, performs the (unencrypted) nonce exchange, derives the
// AES-CBC transport cipher, then performs the (encrypted) handshake -
// mirroring 9seconds/mtg v1's mtproto/protocol.go TelegramProtocol
// function. Tries every candidate address for dc (see telegramInfo.
// Addresses) in order, falling back to the next on any failure, rather
// than giving up after one - live testing found Telegram's own address
// list occasionally includes an endpoint that doesn't complete the
// handshake even though other endpoints for the same DC work fine.
func dialMiddleProxy(ctx context.Context, dc DC) (*middleProxyConn, error) {
	if err := ensureTelegramInfo(ctx); err != nil {
		return nil, err
	}

	addrs := tg.Addresses(dc)
	if len(addrs) == 0 {
		return nil, fmt.Errorf("middleproxy: no known middle-proxy address for dc %d", dc)
	}

	var lastErr error
	for _, addr := range addrs {
		mp, err := dialMiddleProxyAddr(ctx, addr)
		if err == nil {
			return mp, nil
		}
		lastErr = err
	}

	return nil, lastErr
}

func dialMiddleProxyAddr(ctx context.Context, addr string) (*middleProxyConn, error) {
	dialer := net.Dialer{Timeout: middleProxyDialTimeout}
	raw, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("middleproxy: cannot dial middle-proxy server %s: %w", addr, err)
	}

	secret := tg.Secret()

	nonceConn := newRPCFrameConn(raw, seqNoNonce)

	nonceReq, err := newNonceRequest(secret)
	if err != nil {
		raw.Close()
		return nil, err
	}

	if err := nonceConn.Write(nonceReq.bytes()); err != nil {
		raw.Close()
		return nil, fmt.Errorf("middleproxy: cannot send nonce request: %w", err)
	}

	nonceRespRaw, err := nonceConn.Read()
	if err != nil {
		raw.Close()
		return nil, fmt.Errorf("middleproxy: cannot read nonce response: %w", err)
	}

	nonceResp, err := parseNonceResponse(nonceRespRaw)
	if err != nil {
		raw.Close()
		return nil, err
	}

	if err := nonceResp.valid(nonceReq); err != nil {
		raw.Close()
		return nil, err
	}

	localAddr, _ := raw.LocalAddr().(*net.TCPAddr)
	remoteAddr, _ := raw.RemoteAddr().(*net.TCPAddr)

	enc, dec := middleProxyCipher(localAddr, remoteAddr, nonceReq, nonceResp, secret)
	secure := &cbcConn{Conn: raw, enc: enc, dec: dec}
	frameConn := newRPCFrameConn(secure, seqNoHandshake)

	if err := frameConn.Write(handshakeRequestBytes()); err != nil {
		raw.Close()
		return nil, fmt.Errorf("middleproxy: cannot send handshake request: %w", err)
	}

	handshakeResp, err := frameConn.Read()
	if err != nil {
		raw.Close()
		return nil, fmt.Errorf("middleproxy: cannot read handshake response: %w", err)
	}

	if err := validHandshakeResponse(handshakeResp); err != nil {
		raw.Close()
		return nil, err
	}

	return &middleProxyConn{raw: raw, frame: frameConn}, nil
}

func newConnID() [8]byte {
	var id [8]byte
	rand.Read(id[:]) //nolint:errcheck // crypto/rand.Read on a fixed-size buffer never fails in practice
	return id
}

func ipPortBytes(addr *net.TCPAddr) []byte {
	rv := make([]byte, 16+4)
	copy(rv[:16], addr.IP.To16())
	rv[16] = byte(addr.Port)
	rv[17] = byte(addr.Port >> 8)
	return rv
}

// buildProxyRequest wraps one client-originated MTProto message in the
// proxyRequest RPC envelope the middle-proxy server expects, embedding the
// ad tag on every packet - this is the entire reason this package exists;
// every other piece of the middle-proxy protocol works identically with or
// without one. Ported from 9seconds/mtg v1's
// wrappers/packetack/proxy.go (wrapperProxy.Write).
func buildProxyRequest(connID [8]byte, clientAddr, ourAddr *net.TCPAddr, connType ConnectionType, adTag []byte, quickAck bool, packet []byte) []byte {
	flags := proxyRequestFlagsHasAdTag | proxyRequestFlagsMagic | proxyRequestFlagsExtMode2

	switch connType {
	case ConnectionTypeAbridged:
		flags |= proxyRequestFlagsAbridged
	case ConnectionTypeIntermediate:
		flags |= proxyRequestFlagsIntermediate
	case ConnectionTypeSecure:
		flags |= proxyRequestFlagsIntermediate | proxyRequestFlagsPad
	}

	if quickAck {
		flags |= proxyRequestFlagsQuickAck
	}

	prefix := encryptedPrefix[:]
	if len(packet) >= len(prefix) {
		match := true
		for i := range prefix {
			if packet[i] != prefix[i] {
				match = false
				break
			}
		}
		if match {
			flags |= proxyRequestFlagsEncrypted
		}
	}

	buf := make([]byte, 0, 4+4+8+20+20+4+4+1+len(adTag)+len(packet)+4)
	buf = append(buf, tagProxyRequest...)
	buf = append(buf, flags.bytes()...)
	buf = append(buf, connID[:]...)
	buf = append(buf, ipPortBytes(clientAddr)...)
	buf = append(buf, ipPortBytes(ourAddr)...)
	buf = append(buf, proxyRequestExtraSize...)
	buf = append(buf, proxyRequestProxyTag...)
	buf = append(buf, byte(len(adTag)))
	buf = append(buf, adTag...)

	if pad := (4 - len(buf)%4) % 4; pad > 0 {
		buf = append(buf, make([]byte, pad)...)
	}

	buf = append(buf, packet...)

	return buf
}

// relayOptions carries everything one client connection's relay loop needs:
// its detected protocol/DC, the ad tag to embed, and the stats callbacks
// that plug into this backend's existing per-instance eventAccumulator
// (see stats.go) so ad-tag traffic is counted identically to every other
// mtproto instance.
type relayOptions struct {
	AdTag        []byte
	StreamID     string
	Username     string
	Email        string
	OnAuth       func(streamID, username, email string)
	OnTraffic    func(streamID string, n uint, isRead bool)
	OnFinish     func(streamID string)
}

// Relay runs one client connection end to end: dials the middle-proxy
// server for the client's declared DC, then relays discrete MTProto
// messages bidirectionally until either side closes or errors. Blocks until
// the connection ends; callers should run it in its own goroutine per
// accepted client connection.
func Relay(ctx context.Context, client net.Conn, hs obfsHandshakeResult, opts relayOptions) error {
	mp, err := dialMiddleProxy(ctx, hs.dc)
	if err != nil {
		return err
	}
	defer mp.Close()

	if opts.OnAuth != nil {
		opts.OnAuth(opts.StreamID, opts.Username, opts.Email)
	}
	if opts.OnFinish != nil {
		defer opts.OnFinish(opts.StreamID)
	}

	connID := newConnID()
	clientAddr, _ := client.RemoteAddr().(*net.TCPAddr)
	ourAddr, _ := client.LocalAddr().(*net.TCPAddr)

	obfs := &obfsConn{Conn: client, encrypt: hs.encrypt, decrypt: hs.decrypt}

	errCh := make(chan error, 2)

	go func() {
		errCh <- relayClientToMiddle(obfs, mp, hs.connType, connID, clientAddr, ourAddr, opts)
	}()
	go func() {
		errCh <- relayMiddleToClient(obfs, mp, hs.connType, opts)
	}()

	err = <-errCh
	client.Close()
	mp.Close()
	<-errCh

	return err
}

func relayClientToMiddle(client io.Reader, mp *middleProxyConn, connType ConnectionType, connID [8]byte, clientAddr, ourAddr *net.TCPAddr, opts relayOptions) error {
	for {
		packet, quickAck, err := readClientPacket(client, connType)
		if err != nil {
			return fmt.Errorf("middleproxy: client read: %w", err)
		}

		if opts.OnTraffic != nil {
			opts.OnTraffic(opts.StreamID, uint(len(packet)), false)
		}

		req := buildProxyRequest(connID, clientAddr, ourAddr, connType, opts.AdTag, quickAck, packet)
		if err := mp.frame.Write(req); err != nil {
			return fmt.Errorf("middleproxy: middle-proxy write: %w", err)
		}
	}
}

func relayMiddleToClient(client io.Writer, mp *middleProxyConn, connType ConnectionType, opts relayOptions) error {
	for {
		raw, err := mp.frame.Read()
		if err != nil {
			return fmt.Errorf("middleproxy: middle-proxy read: %w", err)
		}

		resp, err := parseProxyResponse(raw)
		if err != nil {
			continue
		}

		switch resp.typ {
		case proxyResponseTypeCloseExt:
			return io.EOF
		case proxyResponseTypeAns, proxyResponseTypeSimpleAck:
			if opts.OnTraffic != nil {
				opts.OnTraffic(opts.StreamID, uint(len(resp.payload)), true)
			}
			simpleAck := resp.typ == proxyResponseTypeSimpleAck
			if err := writeClientPacket(client, connType, resp.payload, simpleAck); err != nil {
				return fmt.Errorf("middleproxy: client write: %w", err)
			}
		}
	}
}
