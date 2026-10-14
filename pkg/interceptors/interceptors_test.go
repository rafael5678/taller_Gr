package interceptors

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestAuthorizeRequiresConfiguredRole(t *testing.T) {
	const method = "/voltbarrio.v1.MeasurementService/SubmitReadings"
	policy := Authorizer{method: {RoleNeighbor}}

	if err := authorize(context.Background(), policy, method); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing role = %v, want unauthenticated", err)
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(roleKey, string(RoleOperator)))
	if err := authorize(ctx, policy, method); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("operator role = %v, want permission denied", err)
	}
	ctx = metadata.NewIncomingContext(context.Background(), metadata.Pairs(roleKey, string(RoleNeighbor)))
	if err := authorize(ctx, policy, method); err != nil {
		t.Fatalf("neighbor role rejected: %v", err)
	}
}
