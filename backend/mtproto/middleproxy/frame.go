package middleproxy

import (
	"bytes"
	"crypto/aes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"net"
)

const (
	rpcFrameMinMessageLength = 12
	rpcFrameMaxMessageLength = 16777216
)

var rpcFramePadding = []byte{0x04, 0x00, 0x00, 0x00}

// rpcFrameConn is the middle-proxy RPC transport's outer framing:
//
//	[ MSGLEN(4) | SEQNO(4) | MSG(...) | CRC32(4) | PADDING(4*x) ]
//
// MSGLEN is the length of MSGLEN+SEQNO+MSG+CRC32. SEQNO must match the
// reader's expected sequence number exactly (strictly increasing per
// direction, starting from the seed each direction is constructed with -
// see rpc.go's SeqNoNonce/SeqNoHandshake). PADDING pads the frame to a
// 16-byte boundary using repeats of the 4-byte padding marker; readers skip
// leading padding-marker "frames" before parsing a real one. Ported from
// 9seconds/mtg v1's wrappers/packet/mtproto_frame.go.
type rpcFrameConn struct {
	conn       net.Conn
	readSeqNo  int32
	writeSeqNo int32
}

func newRPCFrameConn(conn net.Conn, seqNo int32) *rpcFrameConn {
	return &rpcFrameConn{conn: conn, readSeqNo: seqNo, writeSeqNo: seqNo}
}

func (w *rpcFrameConn) Read() ([]byte, error) {
	buf := &bytes.Buffer{}
	sum := crc32.NewIEEE()
	writer := io.MultiWriter(buf, sum)

	for {
		buf.Reset()
		sum.Reset()

		if _, err := io.CopyN(writer, w.conn, 4); err != nil {
			return nil, fmt.Errorf("middleproxy: cannot read frame length: %w", err)
		}

		if !bytes.Equal(buf.Bytes(), rpcFramePadding) {
			break
		}
	}

	messageLength := binary.LittleEndian.Uint32(buf.Bytes())

	if messageLength%4 != 0 || messageLength < rpcFrameMinMessageLength || messageLength > rpcFrameMaxMessageLength {
		return nil, fmt.Errorf("middleproxy: incorrect frame message length %d", messageLength)
	}

	buf.Reset()

	if _, err := io.CopyN(writer, w.conn, int64(messageLength)-4-4); err != nil {
		return nil, fmt.Errorf("middleproxy: cannot read frame body: %w", err)
	}

	var seqNo int32
	binary.Read(bytes.NewReader(buf.Bytes()[:4]), binary.LittleEndian, &seqNo) //nolint:errcheck

	if seqNo != w.readSeqNo {
		return nil, fmt.Errorf("middleproxy: unexpected seq number %d (want %d)", seqNo, w.readSeqNo)
	}

	data := make([]byte, buf.Len()-4)
	copy(data, buf.Bytes()[4:])

	checksumBuf := make([]byte, 4)
	if _, err := io.ReadFull(w.conn, checksumBuf); err != nil {
		return nil, fmt.Errorf("middleproxy: cannot read frame checksum: %w", err)
	}

	checksum := binary.LittleEndian.Uint32(checksumBuf)
	if checksum != sum.Sum32() {
		return nil, fmt.Errorf("middleproxy: frame checksum mismatch")
	}

	w.readSeqNo++

	return data, nil
}

func (w *rpcFrameConn) Write(p []byte) error {
	messageLength := 4 + 4 + len(p) + 4
	paddingLength := (aes.BlockSize - messageLength%aes.BlockSize) % aes.BlockSize

	buf := &bytes.Buffer{}
	buf.Grow(messageLength + paddingLength)

	binary.Write(buf, binary.LittleEndian, uint32(messageLength)) //nolint:errcheck
	binary.Write(buf, binary.LittleEndian, w.writeSeqNo)          //nolint:errcheck
	buf.Write(p)

	checksum := crc32.ChecksumIEEE(buf.Bytes())
	binary.Write(buf, binary.LittleEndian, checksum) //nolint:errcheck
	buf.Write(bytes.Repeat(rpcFramePadding, paddingLength/4))

	w.writeSeqNo++

	_, err := w.conn.Write(buf.Bytes())
	if err != nil {
		return fmt.Errorf("middleproxy: cannot write frame: %w", err)
	}

	return nil
}
