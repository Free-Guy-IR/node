package common

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestTheMTProtoWireNumbersStayReservedAndOldNodesStayCompatible(t *testing.T) {
	enum := BackendType(0).Descriptor()
	if v := enum.Values().ByNumber(4); v != nil {
		t.Fatalf("BackendType 4 still has a value: %s", v.Name())
	}
	var found bool
	for i := 0; i < enum.ReservedRanges().Len(); i++ {
		r := enum.ReservedRanges().Get(i)
		if r[0] <= 4 && 4 <= r[1] {
			found = true
		}
	}
	if !found {
		t.Fatal("BackendType does not reserve 4")
	}
	var names []string
	for i := 0; i < enum.ReservedNames().Len(); i++ {
		names = append(names, string(enum.ReservedNames().Get(i)))
	}
	if len(names) != 1 || names[0] != "MTPROTO" {
		t.Fatalf("BackendType reserved names = %v, want [MTPROTO]", names)
	}

	msg := (&Proxy{}).ProtoReflect().Descriptor()
	if f := msg.Fields().ByNumber(9); f != nil {
		t.Fatalf("Proxy field 9 still exists: %s", f.Name())
	}
	found = false
	for i := 0; i < msg.ReservedRanges().Len(); i++ {
		r := msg.ReservedRanges().Get(i)
		if r[0] <= 9 && 9 < r[1] {
			found = true
		}
	}
	if !found {
		t.Fatal("Proxy does not reserve 9")
	}
	if msg.ReservedNames().Len() != 1 || msg.ReservedNames().Get(0) != protoreflect.Name("mtproto") {
		t.Fatal("Proxy does not reserve the name mtproto")
	}

	for _, n := range []struct {
		name string
		got  protoreflect.FieldNumber
		want protoreflect.FieldNumber
	}{
		{"hysteria2", msg.Fields().ByName("hysteria2").Number(), 7},
		{"open_vpn", msg.Fields().ByName("open_vpn").Number(), 8},
	} {
		if n.got != n.want {
			t.Fatalf("Proxy.%s = %d, want %d", n.name, n.got, n.want)
		}
	}
	if BackendType_SING_BOX != 2 {
		t.Fatalf("SING_BOX = %d, want 2", BackendType_SING_BOX)
	}

	oldNodePayload := []byte{
		0x1a, 0x06, 0x0a, 0x04, 'p', 'a', 's', 's',
		0x4a, 0x0a, 0x0a, 0x02, 'm', 't', 0x12, 0x04, 'a', 'b', 'c', 'd',
	}
	var p Proxy
	if err := proto.Unmarshal(oldNodePayload, &p); err != nil {
		t.Fatalf("a Proxy from the previous image failed to decode: %v", err)
	}
	if p.GetTrojan().GetPassword() != "pass" {
		t.Fatalf("trojan password lost: %q", p.GetTrojan().GetPassword())
	}
	if n := len(p.ProtoReflect().GetUnknown()); n == 0 {
		t.Fatal("field 9 was dropped instead of kept as an unknown field")
	}
	round, err := proto.Marshal(&p)
	if err != nil {
		t.Fatal(err)
	}
	var again Proxy
	if err := proto.Unmarshal(round, &again); err != nil {
		t.Fatal(err)
	}
	if again.GetTrojan().GetPassword() != "pass" {
		t.Fatal("round trip lost the trojan password")
	}
	t.Logf("old-image Proxy decoded: trojan=%q, %d unknown bytes preserved for reserved field 9", p.GetTrojan().GetPassword(), len(p.ProtoReflect().GetUnknown()))
}
