package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	v1 "kratos-micro-layout/api/user/v1"

	"github.com/go-kratos/kratos/v3/errors"
	"github.com/go-kratos/kratos/v3/middleware/validate"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

func TestProtoValidatorPassesValidRequest(t *testing.T) {
	err := ProtoValidator(&v1.LoginRequest{Username: "alice", Password: "secret123"})
	if err != nil {
		t.Fatalf("valid request: unexpected error: %v", err)
	}
}

func TestProtoValidatorIgnoresNonProto(t *testing.T) {
	if err := ProtoValidator(struct{}{}); err != nil {
		t.Fatalf("non-proto value: unexpected error: %v", err)
	}
}

func TestProtoValidatorListsEveryViolation(t *testing.T) {
	// An empty login request violates both custom CEL rules; each violation
	// must surface as "field: description from the proto".
	err := ProtoValidator(&v1.LoginRequest{})
	if err == nil {
		t.Fatal("empty login request: expected a validation error")
	}
	for _, want := range []string{
		"username: username is required",
		"password: password is required",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err.Error(), want)
		}
	}
	if strings.Count(err.Error(), "; ") != 1 {
		t.Errorf("violations should be joined into one line, got %q", err.Error())
	}
}

func TestProtoValidatorCustomLengthMessages(t *testing.T) {
	err := ProtoValidator(&v1.RegisterRequest{
		Username: "ab",
		Email:    "not-an-email",
		Password: "short",
	})
	if err == nil {
		t.Fatal("invalid register request: expected a validation error")
	}
	for _, want := range []string{
		"username is required and must be between 3 and 64 characters",
		"email is required and must be a valid address",
		"password is required and must be between 8 and 72 characters",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err.Error(), want)
		}
	}
}

func TestProtoValidatorIgnoresZeroValueOnPatchFields(t *testing.T) {
	// UpdateProfileRequest declares IGNORE_IF_ZERO_VALUE: a mask-only request
	// leaves the untouched profile fields zero and must still pass.
	req := &v1.UpdateProfileRequest{
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"nickname"}},
	}
	if err := ProtoValidator(req); err != nil {
		t.Fatalf("mask-only patch: unexpected error: %v", err)
	}
	// An empty mask violates the custom CEL rule on update_mask.
	err := ProtoValidator(&v1.UpdateProfileRequest{})
	if err == nil {
		t.Fatal("missing update_mask: expected a validation error")
	}
	if !strings.Contains(err.Error(), "update_mask") {
		t.Errorf("error %q does not mention update_mask", err.Error())
	}
}

// TestValidatorChainKeepsMessageClean walks the whole path a rejected request
// travels: validate.Validator(ProtoValidator) → ErrorEncoder → envelope body.
// kratos rewraps whatever the validator returns with
// errors.BadRequest("VALIDATOR", err.Error()), so returning a kratos error from
// ProtoValidator would leak its "error: code = … reason = …" rendering into the
// message the client sees. This pins the contract end to end.
func TestValidatorChainKeepsMessageClean(t *testing.T) {
	const want = "refresh_token: refresh_token is required"

	mw := validate.Validator(ProtoValidator)
	handler := mw(func(_ context.Context, _ any) (any, error) {
		t.Fatal("handler must not run for an invalid request")
		return nil, nil
	})

	_, err := handler(context.Background(), &v1.RefreshTokenRequest{})
	if err == nil {
		t.Fatal("expected a validation error")
	}
	se := errors.FromError(err)
	if se.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", se.Code)
	}
	if se.Reason != "VALIDATOR" {
		t.Fatalf("reason = %q, want VALIDATOR", se.Reason)
	}
	if se.Message != want {
		t.Fatalf("message = %q, want exactly %q", se.Message, want)
	}

	rec := httptest.NewRecorder()
	ErrorEncoder(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/refresh", nil), err)
	var env Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("envelope is not valid JSON: %v (body: %s)", err, rec.Body.String())
	}
	if env.Code != http.StatusBadRequest {
		t.Errorf("envelope code = %d, want 400", env.Code)
	}
	if env.Message != want {
		t.Errorf("envelope message = %q, want exactly %q", env.Message, want)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("HTTP status = %d, want 200 (the envelope carries the real code)", rec.Code)
	}
}
