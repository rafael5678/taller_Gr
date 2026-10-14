package measurement

import (
	"context"
	"errors"
	"io"

	"github.com/volt-barrio/volt-barrio/gen/go/voltbarrio/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type GRPCServer struct {
	voltbarrio.UnimplementedMeasurementServiceServer
	service *Service
}

func NewGRPCServer(service *Service) (*GRPCServer, error) {
	if service == nil {
		return nil, ErrInvalidReading
	}
	return &GRPCServer{service: service}, nil
}

func (s *GRPCServer) IngestReadings(stream grpc.ClientStreamingServer[voltbarrio.IngestReadingsRequest, voltbarrio.IngestReadingsResponse]) error {
	response := &voltbarrio.IngestReadingsResponse{}
	for {
		request, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return stream.SendAndClose(response)
		}
		if err != nil {
			return err
		}
		if request == nil || request.Reading == nil {
			return status.Error(codes.InvalidArgument, ErrInvalidReading.Error())
		}
		duplicate, err := s.service.Ingest(stream.Context(), request.Reading)
		if err != nil {
			return grpcError(err)
		}
		if duplicate {
			response.DuplicateCount++
		} else {
			response.AcceptedCount++
			response.LastAcceptedReadingId = request.Reading.Reading.ReadingId
		}
	}
}

func (s *GRPCServer) SubmitReadings(ctx context.Context, request *voltbarrio.SubmitReadingsRequest) (*voltbarrio.IngestReadingsResponse, error) {
	if request == nil {
		return nil, status.Error(codes.InvalidArgument, ErrInvalidReading.Error())
	}
	response := &voltbarrio.IngestReadingsResponse{}
	for _, reading := range request.Readings {
		duplicate, err := s.service.Ingest(ctx, reading)
		if err != nil {
			return nil, grpcError(err)
		}
		if duplicate {
			response.DuplicateCount++
		} else {
			response.AcceptedCount++
			response.LastAcceptedReadingId = reading.Reading.ReadingId
		}
	}
	return response, nil
}

func grpcError(err error) error {
	switch {
	case errors.Is(err, ErrRejectionLog):
		return status.Error(codes.Internal, "could not record rejected measurement")
	case errors.Is(err, ErrInvalidReading):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, ErrUnknownKey), errors.Is(err, ErrInvalidSignature):
		return status.Error(codes.Unauthenticated, err.Error())
	case errors.Is(err, ErrReadingConflict):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, ErrRateLimited):
		return status.Error(codes.ResourceExhausted, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
