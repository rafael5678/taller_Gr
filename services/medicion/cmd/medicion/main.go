package main

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/volt-barrio/volt-barrio/gen/go/voltbarrio/v1"
	"github.com/volt-barrio/volt-barrio/pkg/interceptors"
	"github.com/volt-barrio/volt-barrio/pkg/transport"
	"github.com/volt-barrio/volt-barrio/services/medicion/internal/measurement"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
)

func main() {
	if err := run(); err != nil {
		slog.Error("measurement service stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	insecureLocal := strings.EqualFold(os.Getenv("GRPC_INSECURE_LOCAL"), "true")
	certFile := strings.TrimSpace(os.Getenv("TLS_CERT_FILE"))
	keyFile := strings.TrimSpace(os.Getenv("TLS_KEY_FILE"))
	clientCAFile := strings.TrimSpace(os.Getenv("TLS_CLIENT_CA_FILE"))
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	databaseURL = withMeasurementSchema(databaseURL)
	if !insecureLocal && (certFile == "" || keyFile == "" || clientCAFile == "") {
		return errors.New("TLS_CERT_FILE, TLS_KEY_FILE and TLS_CLIENT_CA_FILE are required")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	pingCtx, cancelPing := context.WithTimeout(ctx, 10*time.Second)
	defer cancelPing()
	if err := db.PingContext(pingCtx); err != nil {
		return fmt.Errorf("connect PostgreSQL: %w", err)
	}
	migrationPath := envOr("MEASUREMENT_MIGRATION_FILE", "/app/migrations/003_measurements.sql")
	migration, err := os.ReadFile(migrationPath)
	if err != nil {
		return fmt.Errorf("read measurement database migration: %w", err)
	}
	if _, err := db.ExecContext(pingCtx, string(migration)); err != nil {
		return fmt.Errorf("apply measurement database migration: %w", err)
	}
	store, err := measurement.NewPostgresStore(db)
	if err != nil {
		return err
	}
	keys, err := loadKeys(os.Getenv("MEASUREMENT_PUBLIC_KEYS"))
	if err != nil {
		return err
	}
	limit, err := strconv.Atoi(envOr("MEASUREMENT_RATE_LIMIT", "120"))
	if err != nil || limit < 1 {
		return errors.New("MEASUREMENT_RATE_LIMIT must be a positive integer")
	}
	service, err := measurement.NewService(store, keys, limit, time.Minute)
	if err != nil {
		return err
	}
	grpcService, err := measurement.NewGRPCServer(service)
	if err != nil {
		return err
	}
	metrics := interceptors.NewMetrics()
	roles := interceptors.Authorizer{
		"/voltbarrio.v1.MeasurementService/IngestReadings": {interceptors.RoleNeighbor},
		"/voltbarrio.v1.MeasurementService/SubmitReadings": {interceptors.RoleNeighbor},
	}
	serverOptions := interceptors.ServerOptions(logger, roles, metrics)
	if insecureLocal {
		address := envOr("GRPC_ADDR", ":9090")
		if !strings.HasPrefix(address, "127.0.0.1:") && !strings.HasPrefix(address, "localhost:") {
			return errors.New("GRPC_INSECURE_LOCAL requires GRPC_ADDR to use loopback")
		}
	} else {
		serverCredentials, err := transport.ServerCredentials(certFile, keyFile, clientCAFile)
		if err != nil {
			return fmt.Errorf("load mutual TLS credentials: %w", err)
		}
		serverOptions = append(serverOptions, grpc.Creds(serverCredentials))
	}
	server := grpc.NewServer(serverOptions...)
	voltbarrio.RegisterMeasurementServiceServer(server, grpcService)
	healthServer := health.NewServer()
	healthpb.RegisterHealthServer(server, healthServer)
	healthServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	reflection.Register(server)

	listener, err := net.Listen("tcp", envOr("GRPC_ADDR", ":9090"))
	if err != nil {
		return err
	}
	metricsServer := &http.Server{
		Addr: envOr("METRICS_ADDR", ":9100"), Handler: metrics,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := metricsServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("metrics endpoint failed", "error", err)
		}
	}()
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.Serve(listener) }()

	var restServer *http.Server
	httpErr := make(chan error, 1)
	if httpAddress := strings.TrimSpace(os.Getenv("HTTP_ADDR")); httpAddress != "" {
		credentials := grpc.WithTransportCredentials(insecure.NewCredentials())
		if !insecureLocal {
			clientCredentials, err := transport.ClientCredentials(
				certFile, keyFile, clientCAFile, "medicion",
			)
			if err != nil {
				return fmt.Errorf("load local gRPC client credentials: %w", err)
			}
			credentials = grpc.WithTransportCredentials(clientCredentials)
		}
		connection, err := grpc.NewClient(envOr("GRPC_ADDR", ":9090"), credentials, interceptors.ClientOptions()...)
		if err != nil {
			return fmt.Errorf("create REST gateway gRPC client: %w", err)
		}
		defer connection.Close()
		handler, err := measurement.RESTHandler(
			voltbarrio.NewMeasurementServiceClient(connection), db, metrics,
			os.Getenv("FRONTEND_ORIGIN"),
		)
		if err != nil {
			return err
		}
		restServer = &http.Server{
			Addr: httpAddress, Handler: handler, ReadHeaderTimeout: 5 * time.Second,
		}
		go func() {
			if err := restServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				httpErr <- err
			}
		}()
	}

	select {
	case <-ctx.Done():
	case err := <-serverErr:
		if err != nil {
			return err
		}
	case err := <-httpErr:
		return fmt.Errorf("REST gateway failed: %w", err)
	}
	healthServer.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
	stopped := make(chan struct{})
	go func() {
		server.GracefulStop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		server.Stop()
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	if restServer != nil {
		if err := restServer.Shutdown(shutdownCtx); err != nil {
			return err
		}
	}
	return metricsServer.Shutdown(shutdownCtx)
}

func loadKeys(raw string) (map[string]measurement.SigningKey, error) {
	result := make(map[string]measurement.SigningKey)
	if strings.TrimSpace(raw) == "" {
		return result, nil
	}
	var configured map[string]struct {
		HouseholdID string `json:"household_id"`
		PublicKey   string `json:"public_key"`
	}
	if err := json.Unmarshal([]byte(raw), &configured); err != nil {
		return nil, fmt.Errorf("parse MEASUREMENT_PUBLIC_KEYS JSON: %w", err)
	}
	for id, key := range configured {
		decoded, err := base64.StdEncoding.DecodeString(key.PublicKey)
		if err != nil || len(decoded) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("invalid Ed25519 public key for key id %q", id)
		}
		result[id] = measurement.SigningKey{
			HouseholdID: key.HouseholdID,
			PublicKey:   ed25519.PublicKey(decoded),
		}
	}
	return result, nil
}

func withMeasurementSchema(databaseURL string) string {
	if strings.Contains(databaseURL, "search_path") {
		return databaseURL
	}
	separator := "?"
	if strings.Contains(databaseURL, "?") {
		separator = "&"
	}
	return databaseURL + separator + "options=-csearch_path%3Dvoltbarrio"
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
