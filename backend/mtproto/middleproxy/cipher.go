package middleproxy

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"  //nolint:gosec
	"crypto/sha1" //nolint:gosec
	"crypto/sha256"
	"encoding/binary"
	"net"
)

// obfsFrameLen is the size of a client's obfuscated2 handshake frame:
// [ RANDOM(8) | KEY(32) | IV(16) | MAGIC(4) | DC(2) | RANDOM(2) ].
const obfsFrameLen = 64

const (
	obfsOffsetFirst = 8
	obfsOffsetKey   = obfsOffsetFirst + 32
	obfsOffsetIV    = obfsOffsetKey + 16
	obfsOffsetMagic = obfsOffsetIV + 4
	obfsOffsetDC    = obfsOffsetMagic + 2
)

type obfsFrame struct {
	data [obfsFrameLen]byte
}

func (f *obfsFrame) key() []byte   { return f.data[obfsOffsetFirst:obfsOffsetKey] }
func (f *obfsFrame) iv() []byte    { return f.data[obfsOffsetKey:obfsOffsetIV] }
func (f *obfsFrame) magic() []byte { return f.data[obfsOffsetIV:obfsOffsetMagic] }
func (f *obfsFrame) dc() []byte    { return f.data[obfsOffsetMagic:obfsOffsetDC] }

// invert returns the frame with its key+iv byte-reversed, as used to derive
// the opposite direction's cipher (client's decryptor key/iv == server's
// encryptor key/iv read in reverse, and vice versa - this is how obfuscated2
// derives two independent stream ciphers, one per direction, from a single
// 64-byte handshake frame without an extra round trip).
func (f *obfsFrame) invert() obfsFrame {
	nf := *f
	for i := 0; i < 32+16; i++ {
		nf.data[obfsOffsetFirst+i] = f.data[obfsOffsetIV-1-i]
	}
	return nf
}

func makeAESCTR(key, iv []byte) cipher.Stream {
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
	return cipher.NewCTR(block, iv)
}

// obfsHandshakeResult is what a successful client-facing obfuscated2
// handshake yields: the connection type the client picked, its declared DC,
// and the two independent stream ciphers (encrypt-to-client,
// decrypt-from-client) derived from the handshake frame plus whichever
// candidate secret matched.
type obfsHandshakeResult struct {
	connType ConnectionType
	dc       DC
	encrypt  cipher.Stream // writes to client
	decrypt  cipher.Stream // reads from client
}

// tryObfsSecret attempts one candidate 16-byte secret against an already
// -read 64-byte client frame. It returns ok=false (not an error) for a
// secret that doesn't match, since trying every candidate secret in turn -
// exactly the same "decrypt with each candidate, see if the magic bytes
// come out right" trial matching this repo's mtglib dependency does
// internally for its own multi-secret support - is the intended use.
//
// The returned decrypt/encrypt streams are the SAME cipher.Stream objects
// used to decrypt the 64-byte frame itself, not freshly re-derived ones:
// obfuscated2 is a continuous AES-CTR keystream where the handshake frame's
// own bytes are the first 64 bytes of that stream, so decrypting them here
// necessarily (and correctly) advances the counter to where the client's
// subsequent real traffic picks up. Re-deriving a "fresh" cipher afterward
// would desync decryption from byte 0 instead of byte 64 - this was caught
// as a bug in an earlier draft of this function, ported wrong on first
// pass; ported v1's actual behavior (return the already-advanced ciphers)
// verbatim here.
func tryObfsSecret(fm *obfsFrame, secret []byte) (obfsHandshakeResult, bool) {
	decHasher := sha256.New()
	decHasher.Write(fm.key())
	decHasher.Write(secret)
	decryptor := makeAESCTR(decHasher.Sum(nil)[:32], fm.iv())

	inverted := fm.invert()
	encHasher := sha256.New()
	encHasher.Write(inverted.key())
	encHasher.Write(secret)
	encryptor := makeAESCTR(encHasher.Sum(nil)[:32], inverted.iv())

	var decrypted obfsFrame
	decryptor.XORKeyStream(decrypted.data[:], fm.data[:])

	var connType ConnectionType
	switch {
	case bytes.Equal(decrypted.magic(), connectionTagAbridged):
		connType = ConnectionTypeAbridged
	case bytes.Equal(decrypted.magic(), connectionTagIntermediate):
		connType = ConnectionTypeIntermediate
	case bytes.Equal(decrypted.magic(), connectionTagSecure):
		connType = ConnectionTypeSecure
	default:
		return obfsHandshakeResult{}, false
	}

	dc := DefaultDC
	if len(decrypted.dc()) == 2 {
		v := int16(binary.LittleEndian.Uint16(decrypted.dc()))
		if v != 0 {
			dc = DC(v)
		}
	}

	return obfsHandshakeResult{
		connType: connType,
		dc:       dc,
		encrypt:  encryptor,
		decrypt:  decryptor,
	}, true
}

// middleProxyCipher derives the AES-CBC transport cipher used for the
// physical connection to a middle-proxy server, from the client/server
// nonce exchange plus the shared per-server secret fetched from
// core.telegram.org/getProxySecret. Ported from 9seconds/mtg v1's
// wrappers/stream/mtproto_cipher.go (mtprotoDeriveKeys /
// NewMiddleProxyCipher) - this key schedule is Telegram's own and not
// something this port has any freedom to simplify.
func middleProxyCipher(localAddr, remoteAddr *net.TCPAddr, req *nonceRequest, resp *nonceResponse, secret []byte) (enc, dec cipher.BlockMode) {
	encKey, encIV := deriveMiddleProxyKey("CLIENT", req, resp, localAddr, remoteAddr, secret)
	decKey, decIV := deriveMiddleProxyKey("SERVER", req, resp, localAddr, remoteAddr, secret)

	encBlock, err := aes.NewCipher(encKey)
	if err != nil {
		panic(err)
	}
	decBlock, err := aes.NewCipher(decKey)
	if err != nil {
		panic(err)
	}

	return cipher.NewCBCEncrypter(encBlock, encIV), cipher.NewCBCDecrypter(decBlock, decIV)
}

var emptyIPv4 = [4]byte{}

func reverseBytes(b []byte) []byte {
	out := make([]byte, len(b))
	for i, v := range b {
		out[len(b)-1-i] = v
	}
	return out
}

func deriveMiddleProxyKey(purpose string, req *nonceRequest, resp *nonceResponse, client, remote *net.TCPAddr, secret []byte) ([]byte, []byte) {
	message := bytes.Buffer{}

	message.Write(resp.nonce)
	message.Write(req.nonce)
	message.Write(req.cryptoTS)

	clientIPv4 := emptyIPv4[:]
	serverIPv4 := emptyIPv4[:]

	if v4 := client.IP.To4(); v4 != nil {
		clientIPv4 = reverseBytes(v4)
		serverIPv4 = reverseBytes(remote.IP.To4())
	}

	message.Write(serverIPv4)

	var port [2]byte
	binary.LittleEndian.PutUint16(port[:], uint16(client.Port))
	message.Write(port[:])

	message.WriteString(purpose)

	message.Write(clientIPv4)
	binary.LittleEndian.PutUint16(port[:], uint16(remote.Port))
	message.Write(port[:])
	message.Write(secret)
	message.Write(resp.nonce)

	if client.IP.To4() == nil {
		message.Write(client.IP.To16())
		message.Write(remote.IP.To16())
	}

	message.Write(req.nonce)

	data := message.Bytes()
	md5sum := md5.Sum(data[1:]) //nolint:gosec
	sha1sum := sha1.Sum(data)   //nolint:gosec

	key := append(append([]byte{}, md5sum[:12]...), sha1sum[:]...)
	iv := md5.Sum(data[2:]) //nolint:gosec

	return key, iv[:]
}
