package middleware

import (
	"context"
	"strings"

	"buf.build/go/protovalidate"
	validateext "kratos-micro-layout/pkg/validate/v1"

	"github.com/go-kratos/kratos/v3/errors"
	"github.com/go-kratos/kratos/v3/middleware"
	"google.golang.org/protobuf/proto"
)

// ValidationFailedReason is the kratos error reason carried by requests the
// ProtoValidator rejects. It is transport-generic on purpose: pkg/middleware
// cannot know a domain's api error enum, so services that validate deeper in
// the stack keep using their own reason (e.g. ERROR_REASON_USER_INVALID_ARGUMENT).
const ValidationFailedReason = "VALIDATION_FAILED"

// ProtoValidator validates a request message against the buf.validate rules
// declared in the proto. It adds format rules (email, length, range) on top of
// presence, and runs identically on HTTP and gRPC so the two transports cannot
// drift. protovalidate caches the compiled rules per message type, so the cost
// after the first request is small.
//
// A failed validation becomes a kratos BadRequest whose message lists every
// violated field as "<field path>: <description>", joined with "; " so a client
// can split on it and highlight all offending inputs at once. The description
// is the (validate.v1.error_message) option declared beside the field's rules
// when present — the standard rules carry no message hook of their own — and
// protovalidate's own text otherwise (CEL rules keep their `message`).
// A compilation or other internal protovalidate failure is a server-side bug
// and is passed through unchanged so it surfaces as a 500.
//
// Mount it with Validator, not with kratos' validate.Validator — see there.
func ProtoValidator(v any) error {
	msg, ok := v.(proto.Message)
	if !ok {
		return nil
	}
	err := protovalidate.Validate(msg)
	if err == nil {
		return nil
	}
	verr, ok := err.(*protovalidate.ValidationError)
	if !ok {
		return err
	}
	parts := make([]string, 0, len(verr.Violations))
	for _, violation := range verr.Violations {
		parts = append(parts, violationText(violation))
	}
	return errors.BadRequest(ValidationFailedReason, strings.Join(parts, "; "))
}

// Validator returns the transport middleware that runs ProtoValidator on every
// request and returns its kratos error unchanged. It deliberately does not reuse
// kratos' validate.Validator: that one rewraps whatever the validator returns
// with errors.BadRequest("VALIDATOR", err.Error()), and a kratos error's Error()
// is the full "error: code = … reason = … message = …" rendering — the
// descriptions worded in the proto would reach the client buried inside that
// string, and ValidationFailedReason would never surface.
func Validator() middleware.Middleware {
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			if err := ProtoValidator(req); err != nil {
				return nil, err
			}
			return handler(ctx, req)
		}
	}
}

// violationText renders one violation as "<field path>: <description>". The
// description prefers the field's (validate.v1.error_message) option, read back
// through the descriptor protovalidate attaches to the violation; fields without
// the option fall back to the rule's own text.
func violationText(violation *protovalidate.Violation) string {
	field := protovalidate.FieldPathString(violation.Proto.GetField())
	message := customMessage(violation)
	if message == "" {
		message = violation.Proto.GetMessage()
	}
	if field == "" {
		// A message-level rule: no field path to prefix.
		return message
	}
	return field + ": " + message
}

// customMessage returns the (validate.v1.error_message) option of the violated
// field, or "" when the field declares none.
//
// GetExtension hands back the plain Go value — a string here, not a *string —
// so the assertion must match; asserting a pointer silently yields "" and
// every failure text would fall back to protovalidate's stock wording.
func customMessage(violation *protovalidate.Violation) string {
	fd := violation.FieldDescriptor
	if fd == nil {
		return ""
	}
	opts := fd.Options()
	if opts == nil || !proto.HasExtension(opts, validateext.E_ErrorMessage) {
		return ""
	}
	message, _ := proto.GetExtension(opts, validateext.E_ErrorMessage).(string)
	return message
}
