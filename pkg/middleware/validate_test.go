package middleware

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"buf.build/go/protovalidate"
	v1 "kratos-micro-layout/api/user/v1"

	"github.com/go-kratos/kratos/v3/errors"
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
	// An empty login request trips both required rules; each violation must
	// carry the description the proto declares beside the rule, on a 400.
	err := ProtoValidator(&v1.LoginRequest{})
	if err == nil {
		t.Fatal("empty login request: expected a validation error")
	}
	if !errors.IsBadRequest(err) {
		t.Fatalf("expected a 400 BadRequest, got: %v", err)
	}
	if got := errors.Reason(err); got != ValidationFailedReason {
		t.Fatalf("reason = %q, want %q", got, ValidationFailedReason)
	}
	message := errors.FromError(err).Message
	for _, want := range []string{
		"username: username is required",
		"password: password is required",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("message %q does not contain %q", message, want)
		}
	}
	if strings.Count(message, "; ") != 1 {
		t.Errorf("violations should be joined into one line, got %q", message)
	}
}

func TestProtoValidatorUsesDeclaredErrorMessages(t *testing.T) {
	err := ProtoValidator(&v1.RegisterRequest{
		Username: "ab",
		Email:    "not-an-email",
		Password: "short",
	})
	if err == nil {
		t.Fatal("invalid register request: expected a validation error")
	}
	// The standard rules' stock texts ("must be at least 3 characters",
	// "must be a valid email address") must be replaced by the
	// (validate.v1.error_message) options.
	message := errors.FromError(err).Message
	for _, want := range []string{
		"username is required and must be between 3 and 64 characters",
		"email is required and must be a valid address",
		"password is required and must be between 8 and 72 characters",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("message %q does not contain %q", message, want)
		}
	}
	for _, stock := range []string{"must be at least", "must be a valid email"} {
		if strings.Contains(message, stock) {
			t.Errorf("message %q still carries protovalidate stock text %q", message, stock)
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
	// An unset mask trips `required`, worded by the extension option.
	err := ProtoValidator(&v1.UpdateProfileRequest{})
	if err == nil {
		t.Fatal("missing update_mask: expected a validation error")
	}
	if message := errors.FromError(err).Message; !strings.Contains(message, "update_mask must select at least one of") {
		t.Errorf("message %q does not carry the declared description", message)
	}
	// A set-but-empty mask trips the CEL rule, which carries its own message.
	err = ProtoValidator(&v1.UpdateProfileRequest{UpdateMask: &fieldmaskpb.FieldMask{}})
	if err == nil {
		t.Fatal("empty update_mask: expected a validation error")
	}
	if message := errors.FromError(err).Message; !strings.Contains(message, "update_mask must select at least one of") {
		t.Errorf("message %q does not carry the CEL rule message", message)
	}
}

// TestValidatorChainKeepsMessageClean walks the whole path a rejected request
// travels: Validator() → ErrorEncoder → envelope body. kratos' own
// validate.Validator would rewrap the kratos error with err.Error() — the full
// "error: code = … reason = …" rendering — burying the descriptions worded in
// the proto; this pins that our middleware keeps them intact end to end.
func TestValidatorChainKeepsMessageClean(t *testing.T) {
	const want = "refresh_token: refresh_token is required"

	handler := Validator()(func(_ context.Context, _ any) (any, error) {
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
	if se.Reason != ValidationFailedReason {
		t.Fatalf("reason = %q, want %q", se.Reason, ValidationFailedReason)
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

// TestProtoValidatorPreservesCause pins the parity with kratos' own
// validate.Validator: the structured protovalidate error stays reachable as the
// cause, so logs and the gRPC status keep the per-violation detail even though
// the envelope shows only the joined message.
func TestProtoValidatorPreservesCause(t *testing.T) {
	err := ProtoValidator(&v1.LoginRequest{})
	if err == nil {
		t.Fatal("empty login request: expected a validation error")
	}
	cause := errors.FromError(err).Unwrap()
	if _, ok := cause.(*protovalidate.ValidationError); !ok {
		t.Fatalf("cause = %T (%v), want *protovalidate.ValidationError", cause, cause)
	}
}

// selfValidatingRequest is a request type carrying its own Validate method —
// the interface kratos' validate.Validator also honours.
type selfValidatingRequest struct{ err error }

func (r selfValidatingRequest) Validate() error { return r.err }

// TestValidatorRunsSelfValidateMethod covers the half of kratos' validate.Validator
// that is not about proto rules: a request's own Validate method must still run.
func TestValidatorRunsSelfValidateMethod(t *testing.T) {
	handler := Validator()(func(_ context.Context, _ any) (any, error) {
		t.Fatal("handler must not run for an invalid request")
		return nil, nil
	})

	_, err := handler(context.Background(), selfValidatingRequest{err: fmt.Errorf("order_id must be set")})
	if err == nil {
		t.Fatal("expected a validation error")
	}
	se := errors.FromError(err)
	if se.Code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", se.Code)
	}
	if se.Reason != ValidationFailedReason {
		t.Errorf("reason = %q, want %q", se.Reason, ValidationFailedReason)
	}
	if se.Message != "order_id must be set" {
		t.Errorf("message = %q, want the Validate method's own text", se.Message)
	}
}

// TestValidatorDoesNotRewrapAKratosError pins the deliberate difference from
// kratos' validate.Validator: an error that already is a kratos error keeps its
// own code, reason and message instead of being re-rendered through Error().
func TestValidatorDoesNotRewrapAKratosError(t *testing.T) {
	own := errors.Conflict("ORDER_DUPLICATED", "order_id already exists")
	handler := Validator()(func(_ context.Context, _ any) (any, error) { return nil, nil })

	_, err := handler(context.Background(), selfValidatingRequest{err: own})
	if err == nil {
		t.Fatal("expected a validation error")
	}
	se := errors.FromError(err)
	if se.Reason != "ORDER_DUPLICATED" {
		t.Errorf("reason = %q, want the validator's own ORDER_DUPLICATED", se.Reason)
	}
	if se.Message != "order_id already exists" {
		t.Errorf("message = %q, want the validator's own text", se.Message)
	}
	if se.Code != http.StatusConflict {
		t.Errorf("code = %d, want 409", se.Code)
	}
}
