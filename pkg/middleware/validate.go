package middleware

import (
	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/proto"
)

// ProtoValidator validates a request message against the buf.validate rules
// declared in the proto. It replaces fieldbehavior-only checks, adding format
// rules (email, length, range) on top of presence, and runs identically on HTTP
// and gRPC so the two transports cannot drift. protovalidate caches the compiled
// rules per message type, so the cost after the first request is small.
//
// Pass it to validate.Validator when constructing a kratos server:
//
//	validate.Validator(middleware.ProtoValidator)
func ProtoValidator(v any) error {
	if msg, ok := v.(proto.Message); ok {
		return protovalidate.Validate(msg)
	}
	return nil
}
