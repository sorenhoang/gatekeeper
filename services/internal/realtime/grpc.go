package realtime

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// NewGRPCServer serves the standard health service and gatekeeper.Echo. Echo
// is described by hand with protobuf's well-known StringValue, so the lab
// needs no .proto file or protoc step.
func NewGRPCServer() *grpc.Server {
	s := grpc.NewServer()
	healthpb.RegisterHealthServer(s, health.NewServer())
	s.RegisterService(&echoDesc, echoServer{})
	return s
}

type echoServer struct{}

type echoService interface {
	Say(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error)
}

// Say answers "<caller>: <message>"; the caller comes from the x-user-id
// metadata the gateway sets.
func (echoServer) Say(ctx context.Context, in *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
	who := "anonymous"
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if v := md.Get("x-user-id"); len(v) > 0 && v[0] != "" {
			who = v[0]
		}
	}
	return wrapperspb.String(who + ": " + in.GetValue()), nil
}

var echoDesc = grpc.ServiceDesc{
	ServiceName: "gatekeeper.Echo",
	HandlerType: (*echoService)(nil),
	Methods: []grpc.MethodDesc{{
		MethodName: "Say",
		Handler: func(srv any, ctx context.Context, dec func(any) error, ic grpc.UnaryServerInterceptor) (any, error) {
			in := new(wrapperspb.StringValue)
			if err := dec(in); err != nil {
				return nil, err
			}
			if ic == nil {
				return srv.(echoService).Say(ctx, in)
			}
			info := &grpc.UnaryServerInfo{Server: srv, FullMethod: "/gatekeeper.Echo/Say"}
			return ic(ctx, in, info, func(ctx context.Context, req any) (any, error) {
				return srv.(echoService).Say(ctx, req.(*wrapperspb.StringValue))
			})
		},
	}},
}
