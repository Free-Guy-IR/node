package common

import (
	"bytes"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

func TestMasqueUsesProxyField12(t *testing.T) {
	data, err := proto.Marshal(&Proxy{Masque: &Masque{Pass: "p"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x62, 0x03, 0x0a, 0x01, 'p'}
	if !bytes.Equal(data, want) {
		t.Fatalf("encoded = %x, want %x", data, want)
	}
	num, typ, n := protowire.ConsumeTag(data)
	if n < 0 || num != 12 || typ != protowire.BytesType {
		t.Fatalf("tag = field %d type %d, want field 12 bytes", num, typ)
	}
	if bytes.HasPrefix(data, []byte{0x3a}) {
		t.Fatal("masque must not be encoded on field 7")
	}
}

func TestProxyField7StaysHysteria2(t *testing.T) {
	data := []byte{0x3a, 0x03, 0x0a, 0x01, 'p'}
	decoded := new(Proxy)
	if err := proto.Unmarshal(data, decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.GetHysteria2().GetPassword() != "p" {
		t.Fatalf("field 7 did not decode as Hysteria2: %v", decoded)
	}
	if decoded.GetMasque() != nil {
		t.Fatalf("field 7 decoded as Masque: %v", decoded)
	}
	roundTrip, err := proto.Marshal(&Proxy{Hysteria2: &Hysteria2{Password: "p"}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(roundTrip, data) {
		t.Fatalf("Hysteria2 encoded = %x, want %x", roundTrip, data)
	}
}
