package middleware

import (
	stderrors "errors"
	"strings"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/proto"
)

// ProtoValidator validates a request message against the buf.validate rules
// declared in the proto. It replaces fieldbehavior-only checks, adding format
// rules (email, length, range) on top of presence, and runs identically on HTTP
// and gRPC so the two transports cannot drift. protovalidate caches the compiled
// rules per message type, so the cost after the first request is small.
//
// A failed validation is rendered as one line — every violation as
// "<field path>: <description>" joined with "; " — where the description is the
// custom `message` the proto's CEL rule declares. Clients therefore see exactly
// what to fix, and can split on "; " to highlight all offending inputs at once.
//
// The returned error is deliberately a *plain* error, not a kratos one:
// validate.Validator wraps whatever the validator returns with
// errors.BadRequest("VALIDATOR", err.Error()), and kratos' (*errors.Error).Error()
// renders the full "error: code = … reason = … message = …" form. Returning a
// kratos error here would nest that whole string into the message the client
// sees. Leaving the wrapping to kratos keeps the message clean and the status a
// 400 with reason VALIDATOR (the envelope does not surface reason anyway).
// A compilation or other internal protovalidate failure is a server-side bug and
// is passed through unchanged so it surfaces as a 500.
//
// Pass it to validate.Validator when constructing a kratos server:
//
//	validate.Validator(middleware.ProtoValidator)
func ProtoValidator(v any) error {
	msg, ok := v.(proto.Message)
	if !ok {
		return nil
	}
	err := protovalidate.Validate(msg)
	if err == nil {
		return nil
	}
	var verr *protovalidate.ValidationError
	if stderrors.As(err, &verr) {
		parts := make([]string, 0, len(verr.Violations))
		for _, violation := range verr.Violations {
			// Violation.String() renders "<field path>: <message>".
			parts = append(parts, violation.String())
		}
		return stderrors.New(strings.Join(parts, "; "))
	}
	return err
}
