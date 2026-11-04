package patches

import (
	"context"
	"go/constant"
	"go/token"
	"go/types"
	"net"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const commanderPatchFile = "xray-commander-message-size.patch"

var (
	commanderSizeConstant = regexp.MustCompile(`^const commanderMaxMessageSize = (.+)$`)
	commanderServerOption = regexp.MustCompile(`grpc\.(\w+)\(([^()]*)\)`)
)

type commanderPatch struct {
	size    int
	options []grpc.ServerOption
	names   []string
}

func evalInt(t *testing.T, expr string) int {
	t.Helper()
	tv, err := types.Eval(token.NewFileSet(), nil, token.NoPos, expr)
	if err != nil || tv.Value == nil || tv.Value.Kind() != constant.Int {
		t.Fatalf("cannot evaluate %q from %s: %v", expr, commanderPatchFile, err)
	}
	v, ok := constant.Int64Val(tv.Value)
	if !ok {
		t.Fatalf("%q from %s does not fit in int64", expr, commanderPatchFile)
	}
	return int(v)
}

func loadCommanderPatch(t *testing.T) commanderPatch {
	t.Helper()
	raw, err := os.ReadFile(commanderPatchFile)
	if err != nil {
		t.Fatal(err)
	}
	var added []string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			added = append(added, strings.TrimSpace(line[1:]))
		}
	}
	p := commanderPatch{}
	for _, line := range added {
		if m := commanderSizeConstant.FindStringSubmatch(line); m != nil {
			p.size = evalInt(t, m[1])
		}
	}
	if p.size == 0 {
		t.Fatalf("%s adds no commanderMaxMessageSize constant", commanderPatchFile)
	}
	inServer := false
	for _, line := range added {
		if strings.HasPrefix(line, "c.server = grpc.NewServer(") {
			inServer = true
		}
		if !inServer {
			continue
		}
		for _, m := range commanderServerOption.FindAllStringSubmatch(line, -1) {
			if m[1] == "NewServer" {
				continue
			}
			arg := strings.TrimSpace(m[2])
			value := p.size
			if arg != "commanderMaxMessageSize" {
				value = evalInt(t, arg)
			}
			switch m[1] {
			case "MaxRecvMsgSize":
				p.options = append(p.options, grpc.MaxRecvMsgSize(value))
			case "MaxSendMsgSize":
				p.options = append(p.options, grpc.MaxSendMsgSize(value))
			default:
				t.Fatalf("%s passes grpc.%s to the commander server, which this test does not model", commanderPatchFile, m[1])
			}
			p.names = append(p.names, m[1])
		}
		if strings.HasSuffix(line, ")") && !strings.HasSuffix(line, "),") {
			inServer = false
		}
	}
	return p
}

type commanderSizeServer interface {
	Make(context.Context, *wrapperspb.Int64Value) (*wrapperspb.BytesValue, error)
	Count(context.Context, *wrapperspb.BytesValue) (*wrapperspb.Int64Value, error)
}

type commanderSizeService struct{}

func (commanderSizeService) Make(_ context.Context, in *wrapperspb.Int64Value) (*wrapperspb.BytesValue, error) {
	return wrapperspb.Bytes(make([]byte, in.GetValue())), nil
}

func (commanderSizeService) Count(_ context.Context, in *wrapperspb.BytesValue) (*wrapperspb.Int64Value, error) {
	return wrapperspb.Int64(int64(len(in.GetValue()))), nil
}

func commanderSizeHandler[In any, PIn interface{ *In }](call func(commanderSizeServer, context.Context, PIn) (any, error)) grpc.MethodHandler {
	return func(srv any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
		in := PIn(new(In))
		if err := dec(in); err != nil {
			return nil, err
		}
		return call(srv.(commanderSizeServer), ctx, in)
	}
}

func startPatchedCommander(t *testing.T, p commanderPatch) *grpc.ClientConn {
	t.Helper()
	server := grpc.NewServer(p.options...)
	server.RegisterService(&grpc.ServiceDesc{
		ServiceName: "commandertest.Size",
		HandlerType: (*commanderSizeServer)(nil),
		Methods: []grpc.MethodDesc{
			{
				MethodName: "Make",
				Handler: commanderSizeHandler(func(s commanderSizeServer, ctx context.Context, in *wrapperspb.Int64Value) (any, error) {
					return s.Make(ctx, in)
				}),
			},
			{
				MethodName: "Count",
				Handler: commanderSizeHandler(func(s commanderSizeServer, ctx context.Context, in *wrapperspb.BytesValue) (any, error) {
					return s.Count(ctx, in)
				}),
			},
		},
	}, commanderSizeService{})
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve(lis)
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(256<<20), grpc.MaxCallSendMsgSize(256<<20)),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func callCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestCommanderPatchKeepsOnlyTheReceiveLimit(t *testing.T) {
	p := loadCommanderPatch(t)
	if p.size != 64<<20 {
		t.Fatalf("commanderMaxMessageSize is %d, want %d", p.size, 64<<20)
	}
	if strings.Join(p.names, ",") != "MaxRecvMsgSize" {
		t.Fatalf("the commander server gets grpc options %v, want only MaxRecvMsgSize", p.names)
	}
}

func TestCommanderPatchSendsAResponseLargerThanItsReceiveLimit(t *testing.T) {
	p := loadCommanderPatch(t)
	conn := startPatchedCommander(t, p)
	want := int64(p.size + 1<<20)
	out := new(wrapperspb.BytesValue)
	if err := conn.Invoke(callCtx(t), "/commandertest.Size/Make", wrapperspb.Int64(want), out, grpc.WaitForReady(true)); err != nil {
		t.Fatalf("a %d-byte commander response failed with options %v: %v", want, p.names, err)
	}
	if int64(len(out.GetValue())) != want {
		t.Fatalf("got a %d-byte response, want %d", len(out.GetValue()), want)
	}
}

func TestCommanderPatchKeepsTheRaisedReceiveLimit(t *testing.T) {
	p := loadCommanderPatch(t)
	conn := startPatchedCommander(t, p)
	accepted := 5 << 20
	out := new(wrapperspb.Int64Value)
	if err := conn.Invoke(callCtx(t), "/commandertest.Size/Count", wrapperspb.Bytes(make([]byte, accepted)), out, grpc.WaitForReady(true)); err != nil {
		t.Fatalf("a %d-byte request above the 4 MiB default failed: %v", accepted, err)
	}
	if out.GetValue() != int64(accepted) {
		t.Fatalf("the server saw %d bytes, want %d", out.GetValue(), accepted)
	}
	err := conn.Invoke(callCtx(t), "/commandertest.Size/Count", wrapperspb.Bytes(make([]byte, p.size)), out, grpc.WaitForReady(true))
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("a request over the %d-byte receive limit returned %v, want ResourceExhausted", p.size, err)
	}
}
