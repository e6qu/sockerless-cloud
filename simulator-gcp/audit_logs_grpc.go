package main

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// auditUnaryInterceptor audits the gRPC calls of the services that write
// Cloud Audit Logs, the same calls their REST bindings audit.
func auditUnaryInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if err := auditLoadRPCs(); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	rpc, ok := auditRPCsByName[strings.ReplaceAll(strings.TrimPrefix(info.FullMethod, "/"), "/", ".")]
	if !ok {
		return handler(ctx, req)
	}
	at := time.Now()
	resp, err := handler(ctx, req)
	request, ok := auditMessageJSON(req)
	if !ok {
		return resp, err
	}
	var response map[string]any
	callStatus := auditStatus(0, "")
	if err != nil {
		s := status.Convert(err)
		callStatus = auditStatus(int(s.Code()), s.Message())
	} else if decoded, ok := auditMessageJSON(resp); ok {
		response = decoded
	}
	rec := auditOnePlatformRecord(rpc, request, response, callStatus, auditCaller{})
	rec.at = at
	rec.caller = auditGRPCCaller(ctx)
	emitAuditCall(rec)
	return resp, err
}

// auditMessageJSON is a message in the JSON form its REST binding speaks.
func auditMessageJSON(message any) (map[string]any, bool) {
	m, ok := message.(proto.Message)
	if !ok {
		return nil, false
	}
	raw, err := protojson.Marshal(m)
	if err != nil {
		return nil, false
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, false
	}
	return out, true
}

func auditGRPCCaller(ctx context.Context) auditCaller {
	var authorization, userAgent, remote string
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if values := md.Get("authorization"); len(values) > 0 {
			authorization = values[0]
		}
		if values := md.Get("user-agent"); len(values) > 0 {
			userAgent = values[0]
		}
	}
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		remote = p.Addr.String()
	}
	return auditCallerFromCredential(authorization, remote, userAgent)
}
