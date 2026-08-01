package middleproxy

import (
	"bytes"
	"crypto/cipher"
	"encoding/binary"
	"fmt"
	"io"
	"math/rand"
	"net"
)

func randIntn(n int) int {
	return rand.Intn(n) //nolint:gosec // padding length only, not security-sensitive
}

// obfsConn wraps a client net.Conn with the two independent AES-CTR streams
// derived from its obfuscated2 handshake, presenting a plain io.Reader/
// io.Writer of decrypted/encrypted bytes to the client-protocol framers in
// this file.
type obfsConn struct {
	net.Conn
	encrypt cipher.Stream
	decrypt cipher.Stream
}

func (c *obfsConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.decrypt.XORKeyStream(p[:n], p[:n])
	}
	return n, err
}

func (c *obfsConn) Write(p []byte) (int, error) {
	out := make([]byte, len(p))
	c.encrypt.XORKeyStream(out, p)
	return c.Conn.Write(out)
}

const (
	clientAbridgedSmallPacketLength = 0x7f
	clientAbridgedQuickAckLength    = 0x80
	clientAbridgedLargePacketLength = 16777216

	clientIntermediateQuickAckLength = 0x80000000
)

// readClientPacket reads one discrete MTProto message from the client,
// deframing whichever wire format connType selected. Ported from
// 9seconds/mtg v1's wrappers/packetack/client_{abridged,intermediate,
// intermediate_secure}.go. The returned quickAck flag mirrors a client
// request for a "quick ack" - this port always answers with a normal (not
// quick) ack, so the flag is parsed but otherwise unused; a client that
// asked for a quick ack still gets a correct, if not maximally fast,
// response.
func readClientPacket(r io.Reader, connType ConnectionType) (data []byte, quickAck bool, err error) {
	switch connType {
	case ConnectionTypeAbridged:
		return readClientAbridged(r)
	case ConnectionTypeIntermediate, ConnectionTypeSecure:
		return readClientIntermediate(r, connType == ConnectionTypeSecure)
	default:
		return nil, false, fmt.Errorf("middleproxy: unknown client connection type %d", connType)
	}
}

func readClientAbridged(r io.Reader) ([]byte, bool, error) {
	lenBuf := make([]byte, 1)
	if _, err := io.ReadFull(r, lenBuf); err != nil {
		return nil, false, fmt.Errorf("middleproxy: cannot read abridged length: %w", err)
	}

	msgLength := uint32(lenBuf[0])
	quickAck := false
	if msgLength >= clientAbridgedQuickAckLength {
		quickAck = true
		msgLength -= clientAbridgedQuickAckLength
	}

	if msgLength == clientAbridgedSmallPacketLength {
		buf := make([]byte, 3)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, false, fmt.Errorf("middleproxy: cannot read abridged extended length: %w", err)
		}
		msgLength = uint32(buf[0]) | uint32(buf[1])<<8 | uint32(buf[2])<<16
	}

	msgLength *= 4

	data := make([]byte, msgLength)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, false, fmt.Errorf("middleproxy: cannot read abridged message: %w", err)
	}

	return data, quickAck, nil
}

func writeClientAbridged(w io.Writer, packet []byte, simpleAck bool) error {
	if simpleAck {
		_, err := w.Write(reverseBytes(packet))
		return err
	}

	if len(packet)%4 != 0 {
		return fmt.Errorf("middleproxy: abridged packet length %d not a multiple of 4", len(packet))
	}

	packetLength := len(packet) / 4

	switch {
	case packetLength < clientAbridgedSmallPacketLength:
		if _, err := w.Write(append([]byte{byte(packetLength)}, packet...)); err != nil {
			return fmt.Errorf("middleproxy: cannot write abridged small packet: %w", err)
		}
	case packetLength < clientAbridgedLargePacketLength:
		buf := &bytes.Buffer{}
		buf.WriteByte(clientAbridgedSmallPacketLength)
		buf.WriteByte(byte(packetLength))
		buf.WriteByte(byte(packetLength >> 8))
		buf.WriteByte(byte(packetLength >> 16))
		buf.Write(packet)
		if _, err := w.Write(buf.Bytes()); err != nil {
			return fmt.Errorf("middleproxy: cannot write abridged large packet: %w", err)
		}
	default:
		return fmt.Errorf("middleproxy: packet too large for abridged framing: %d bytes", len(packet))
	}

	return nil
}

func readClientIntermediate(r io.Reader, secure bool) ([]byte, bool, error) {
	lenBuf := make([]byte, 4)
	if _, err := io.ReadFull(r, lenBuf); err != nil {
		return nil, false, fmt.Errorf("middleproxy: cannot read intermediate length: %w", err)
	}

	length := binary.LittleEndian.Uint32(lenBuf)
	quickAck := false
	if length > clientIntermediateQuickAckLength {
		quickAck = true
		length -= clientIntermediateQuickAckLength
	}

	data := make([]byte, length)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, false, fmt.Errorf("middleproxy: cannot read intermediate message: %w", err)
	}

	if secure {
		// The "secure" (padded intermediate) variant appends 0-3 random
		// padding bytes; trim to the nearest multiple of 4 to recover the
		// real message, matching v1's wrapperClientIntermediateSecure.Read.
		trimmed := len(data) - (len(data) % 4)
		data = data[:trimmed]
	}

	return data, quickAck, nil
}

func writeClientIntermediate(w io.Writer, packet []byte, simpleAck, secure bool) error {
	if simpleAck {
		_, err := w.Write(packet)
		return err
	}

	padding := 0
	if secure {
		padding = randIntn(4)
	}

	buf := &bytes.Buffer{}
	buf.Grow(4 + len(packet) + padding)
	binary.Write(buf, binary.LittleEndian, uint32(len(packet)+padding)) //nolint:errcheck
	buf.Write(packet)
	if padding > 0 {
		buf.Write(make([]byte, padding))
	}

	if _, err := w.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("middleproxy: cannot write intermediate packet: %w", err)
	}

	return nil
}

// writeClientPacket writes one discrete MTProto message to the client,
// framing it per connType. simpleAck mirrors the middle-proxy server's
// "simple ack" response type, which every client protocol frames
// differently (or, for abridged, byte-reverses instead of length-prefixing
// - matching Telegram's own client-side abridged simple-ack convention).
func writeClientPacket(w io.Writer, connType ConnectionType, packet []byte, simpleAck bool) error {
	switch connType {
	case ConnectionTypeAbridged:
		return writeClientAbridged(w, packet, simpleAck)
	case ConnectionTypeIntermediate:
		return writeClientIntermediate(w, packet, simpleAck, false)
	case ConnectionTypeSecure:
		return writeClientIntermediate(w, packet, simpleAck, true)
	default:
		return fmt.Errorf("middleproxy: unknown client connection type %d", connType)
	}
}
