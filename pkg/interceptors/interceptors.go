package interceptors

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type Role string

const (
	RoleNeighbor   Role = "neighbor"
	RoleOperator   Role = "operator"
	RoleRegulator  Role = "regulator"
	requestIDKey        = "x-request-id"
	roleKey             = "x-voltbarrio-role"
)

type Authorizer map[string][]Role

func (a Authorizer) allowed(method string, role Role) bool {
	for _, allowed := range a[method] {
		if role == allowed {
			return true
		}
	}
	return false
}

type methodMetrics struct {
	calls       atomic.Uint64
	errors      atomic.Uint64
	durationNS  atomic.Uint64
}

type Metrics struct {
	mu      sync.RWMutex
	methods map[string]*methodMetrics
}

func NewMetrics() *Metrics {
	return &Metrics{methods: make(map[string]*methodMetrics)}
}

func (m *Metrics) method(name string) *methodMetrics {
	m.mu.RLock()
	item := m.methods[name]
	m.mu.RUnlock()
	if item != nil {
		return item
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if item = m.methods[name]; item == nil {
		item = &methodMetrics{}
		m.methods[name] = item
	}
	return item
}

func (m *Metrics) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	for name, item := range m.methods {
		method := strings.NewReplacer("/", "_", ".", "_").Replace(name)
		_, _ = w.Write([]byte("grpc_server_calls_total{method=\"" + method + "\"} " + strconv.FormatUint(item.calls.Load(), 10) + "\n"))
		_, _ = w.Write([]byte("grpc_server_errors_total{method=\"" + method + "\"} " + strconv.FormatUint(item.errors.Load(), 10) + "\n"))
		_, _ = w.Write([]byte("grpc_server_duration_seconds_sum{method=\"" + method + "\"} " + strconv.FormatFloat(float64(item.durationNS.Load())/float64(time.Second), 'f', 9, 64) + "\n"))
	}
}

func ServerOptions(logger *slog.Logger, authorizer Authorizer, metrics *Metrics) []grpc.ServerOption {
	if logger == nil {
		logger = slog.Default()
	}
	if metrics == nil {
		metrics = NewMetrics()
	}
	return []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(unary(logger, authorizer, metrics)),
		grpc.ChainStreamInterceptor(stream(logger, authorizer, metrics)),
	}
}

func unary(logger *slog.Logger, authorizer Authorizer, metrics *Metrics) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (response any, resultErr error) {
		ctx, requestID := withRequestID(ctx)
		if err := grpc.SetHeader(ctx, metadata.Pairs(requestIDKey, requestID)); err != nil {
			logger.WarnContext(ctx, "could not return request ID header", "method", info.FullMethod, "error", err)
		}
		start := time.Now()
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.ErrorContext(ctx, "gRPC panic recovered", "method", info.FullMethod, "request_id", requestID, "panic", recovered, "stack", string(debug.Stack()))
				resultErr = status.Error(codes.Internal, "internal server error")
				record(logger, metrics, info.FullMethod, requestID, start, resultErr)
			}
		}()
		if err := authorize(ctx, authorizer, info.FullMethod); err != nil {
			record(logger, metrics, info.FullMethod, requestID, start, err)
			return nil, err
		}
		response, err := handler(ctx, req)
		record(logger, metrics, info.FullMethod, requestID, start, err)
		return response, err
	}
}

func stream(logger *slog.Logger, authorizer Authorizer, metrics *Metrics) grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (result error) {
		ctx, requestID := withRequestID(stream.Context())
		if err := stream.SetHeader(metadata.Pairs(requestIDKey, requestID)); err != nil {
			logger.WarnContext(ctx, "could not return request ID header", "method", info.FullMethod, "error", err)
		}
		start := time.Now()
		wrapped := &serverStream{ServerStream: stream, ctx: ctx}
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.ErrorContext(ctx, "gRPC stream panic recovered", "method", info.FullMethod, "request_id", requestID, "panic", recovered, "stack", string(debug.Stack()))
				result = status.Error(codes.Internal, "internal server error")
				record(logger, metrics, info.FullMethod, requestID, start, result)
			}
		}()
		if err := authorize(ctx, authorizer, info.FullMethod); err != nil {
			record(logger, metrics, info.FullMethod, requestID, start, err)
			return err
		}
		result = handler(srv, wrapped)
		record(logger, metrics, info.FullMethod, requestID, start, result)
		return result
	}
}

func ClientOptions() []grpc.DialOption {
	return []grpc.DialOption{
		grpc.WithChainUnaryInterceptor(func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
			return invoker(OutgoingRequestID(ctx), method, req, reply, cc, opts...)
		}),
		grpc.WithChainStreamInterceptor(func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
			return streamer(OutgoingRequestID(ctx), desc, cc, method, opts...)
		}),
	}
}

func authorize(ctx context.Context, authorizer Authorizer, method string) error {
	roles := authorizer[method]
	if len(roles) == 0 {
		return status.Error(codes.PermissionDenied, "method has no role policy")
	}
	md, _ := metadata.FromIncomingContext(ctx)
	values := md.Get(roleKey)
	if len(values) != 1 {
		return status.Error(codes.Unauthenticated, "exactly one authenticated role is required")
	}
	if !authorizer.allowed(method, Role(values[0])) {
		return status.Error(codes.PermissionDenied, "role is not authorized for this method")
	}
	return nil
}

func withRequestID(ctx context.Context) (context.Context, string) {
	md, _ := metadata.FromIncomingContext(ctx)
	ids := md.Get(requestIDKey)
	if len(ids) == 1 && strings.TrimSpace(ids[0]) != "" && len(ids[0]) <= 128 {
		return context.WithValue(ctx, requestIDContextKey{}, ids[0]), ids[0]
	}
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return ctx, "unavailable"
	}
	id := hex.EncodeToString(bytes[:])
	return context.WithValue(ctx, requestIDContextKey{}, id), id
}

type requestIDContextKey struct{}

func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDContextKey{}).(string)
	return id
}

func OutgoingRequestID(ctx context.Context) context.Context {
	if id := RequestID(ctx); id != "" {
		return metadata.AppendToOutgoingContext(ctx, requestIDKey, id)
	}
	return ctx
}

type serverStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *serverStream) Context() context.Context { return s.ctx }

func record(logger *slog.Logger, metrics *Metrics, method, requestID string, started time.Time, err error) {
	elapsed := time.Since(started)
	item := metrics.method(method)
	item.calls.Add(1)
	item.durationNS.Add(uint64(elapsed))
	attrs := []any{"method", method, "request_id", requestID, "duration", elapsed}
	if err != nil {
		item.errors.Add(1)
		logger.Warn("gRPC request failed", append(attrs, "error", err)...)
		return
	}
	logger.Info("gRPC request completed", attrs...)
}
