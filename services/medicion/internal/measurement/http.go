package measurement

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/volt-barrio/volt-barrio/gen/go/voltbarrio/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

const maxRESTBodyBytes = 1 << 20

func RESTHandler(client voltbarrio.MeasurementServiceClient, database interface {
	PingContext(context.Context) error
}, metrics http.Handler, allowedOrigin string) (http.Handler, error) {
	if client == nil || database == nil || metrics == nil {
		return nil, ErrInvalidReading
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/readings:batch", func(w http.ResponseWriter, r *http.Request) {
		body := http.MaxBytesReader(w, r.Body, maxRESTBodyBytes)
		defer body.Close()
		payload, err := io.ReadAll(body)
		if err != nil {
			http.Error(w, "invalid or oversized request body", http.StatusBadRequest)
			return
		}
		request := &voltbarrio.SubmitReadingsRequest{}
		if err := protojson.Unmarshal(payload, request); err != nil {
			http.Error(w, "invalid protobuf JSON request", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		ctx = metadata.AppendToOutgoingContext(ctx,
			"x-voltbarrio-role", "neighbor",
			"x-request-id", requestID(r),
		)
		response, err := client.SubmitReadings(ctx, request, grpc.WaitForReady(false))
		if err != nil {
			writeGRPCError(w, err)
			return
		}
		result, err := protojson.Marshal(response)
		if err != nil {
			http.Error(w, "could not encode response", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(result)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := database.PingContext(ctx); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.Handle("GET /metrics", metrics)
	return requestIDMiddleware(corsMiddleware(mux, allowedOrigin)), nil
}

func corsMiddleware(next http.Handler, allowedOrigin string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			if !originAllowed(origin, allowedOrigin) {
				http.Error(w, "origin is not allowed", http.StatusForbidden)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Request-Id")
			w.Header().Set("Access-Control-Max-Age", "600")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func originAllowed(origin, allowed string) bool {
	origin = strings.TrimRight(strings.TrimSpace(origin), "/")
	if origin == "" || strings.TrimSpace(allowed) == "" {
		return false
	}
	for _, item := range strings.Split(allowed, ",") {
		if strings.TrimRight(strings.TrimSpace(item), "/") == origin {
			return true
		}
	}
	return false
}

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := requestID(r)
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r)
	})
}

func requestID(r *http.Request) string {
	if id := strings.TrimSpace(r.Header.Get("X-Request-Id")); id != "" && len(id) <= 128 {
		return id
	}
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "request-id-unavailable"
	}
	return hex.EncodeToString(value[:])
}

func writeGRPCError(w http.ResponseWriter, err error) {
	code := status.Code(err)
	httpStatus := http.StatusInternalServerError
	switch code {
	case codes.InvalidArgument:
		httpStatus = http.StatusBadRequest
	case codes.Unauthenticated:
		httpStatus = http.StatusUnauthorized
	case codes.PermissionDenied:
		httpStatus = http.StatusForbidden
	case codes.AlreadyExists:
		httpStatus = http.StatusConflict
	case codes.ResourceExhausted:
		httpStatus = http.StatusTooManyRequests
	case codes.DeadlineExceeded:
		httpStatus = http.StatusGatewayTimeout
	case codes.Unavailable:
		httpStatus = http.StatusServiceUnavailable
	}
	if errors.Is(err, context.DeadlineExceeded) {
		httpStatus = http.StatusGatewayTimeout
	}
	http.Error(w, status.Convert(err).Message(), httpStatus)
}
