package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"reflect"

	"github.com/go-kratos/kratos/v3/errors"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Envelope is the unified HTTP response wrapper. Every JSON reply — success or
// failure — shares this shape so clients parse a single structure:
//
//	{"code":0,"message":"","reason":"","data":{...},"metadata":{}}
//
// Code is 0 on success and the kratos/gRPC status code on failure; Reason
// carries the API error enum (empty on success); Data holds the resource on
// success and null on failure. This is the HTTP-only convention: gRPC keeps its
// native status codes and is not enveloped.
type Envelope struct {
	Code     int32             `json:"code"`
	Message  string            `json:"message"`
	Reason   string            `json:"reason,omitempty"`
	Data     json.RawMessage   `json:"data"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// nullData is the literal JSON null embedded under "data" when there is no
// payload (errors, and empty replies).
var nullData = json.RawMessage("null")

// Proto messages are serialised with protojson — the same mapping the wider
// proto ecosystem uses (gRPC-Gateway, Google APIs, buf) — rather than kratos
// v3's built-in "json" codec, which falls back to encoding/json and would emit
// timestamps as {"seconds":…,"nanos":…} and enums as bare integers. protojson
// gives RFC 3339 timestamps, enums by name and int64 as strings. The same
// options are applied on the way in (RequestDecoder) so a client can read a
// field from a reply and send it straight back.
var (
	protoMarshalOpts = protojson.MarshalOptions{
		UseProtoNames:   true,  // snake_case, matching the .proto and OpenAPI spec
		EmitUnpopulated: false, // omit zero-value fields for compact replies
	}
	protoUnmarshalOpts = protojson.UnmarshalOptions{
		DiscardUnknown: true, // tolerate fields this build of the API doesn't know
	}
	protoMessageType = reflect.TypeOf((*proto.Message)(nil)).Elem()
)

// ResponseEncoder wraps a successful reply in the unified envelope. The payload
// is marshalled with protojson and embedded verbatim under "data". Pass it to
// http.ResponseEncoder when constructing a kratos HTTP server.
func ResponseEncoder(w http.ResponseWriter, _ *http.Request, v any) error {
	if v == nil {
		return writeEnvelope(w, Envelope{Code: 0, Data: nullData})
	}
	data, err := marshalData(v)
	if err != nil {
		return err
	}
	return writeEnvelope(w, Envelope{Code: 0, Data: data})
}

// marshalData renders the reply payload: proto messages go through protojson,
// anything else (rare — a plain struct or map) falls back to encoding/json.
func marshalData(v any) (json.RawMessage, error) {
	if msg, ok := v.(proto.Message); ok {
		return protoMarshalOpts.Marshal(msg)
	}
	return json.Marshal(v)
}

// RequestDecoder is the HTTP body decoder. It mirrors kratos's default decoder
// but routes proto messages through protojson so inbound JSON uses the same
// conventions as the replies (RFC 3339 timestamps, enums by name or number,
// unknown fields tolerated). Query-string and path-variable binding are handled
// by kratos's separate decoders and are unaffected. Pass it to
// http.RequestDecoder when constructing a kratos HTTP server.
func RequestDecoder(r *http.Request, v any) error {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return errors.BadRequest("CODEC", err.Error())
	}
	// Reset the body so later middleware/handlers can re-read it if needed.
	r.Body = io.NopCloser(bytes.NewBuffer(data))
	if len(data) == 0 {
		return nil
	}
	if err := unmarshalData(data, v); err != nil {
		return errors.BadRequest("CODEC", err.Error())
	}
	return nil
}

// unmarshalData decodes a request body into v. The generated HTTP handlers bind
// either a whole message (*Msg, e.g. ctx.Bind(&in)) or a nested message field
// (**Msg, e.g. ctx.Bind(&in.User)); both are handled here, with a plain
// encoding/json fallback for non-proto targets.
func unmarshalData(data []byte, v any) error {
	if msg, ok := v.(proto.Message); ok {
		return protoUnmarshalOpts.Unmarshal(data, msg)
	}
	rv := reflect.ValueOf(v)
	if rv.IsValid() && rv.Kind() == reflect.Pointer && !rv.IsNil() {
		elem := rv.Type().Elem()
		if elem.Kind() == reflect.Pointer && elem.Implements(protoMessageType) {
			target := rv.Elem()
			if target.IsNil() {
				target.Set(reflect.New(elem.Elem()))
			}
			return protoUnmarshalOpts.Unmarshal(data, target.Interface().(proto.Message))
		}
	}
	return json.Unmarshal(data, v)
}

// ErrorEncoder renders a handler error into the unified envelope. The kratos
// error is decomposed into code/reason/message/metadata so the body carries the
// same information the default encoder would, just wrapped. Pass it to
// http.ErrorEncoder when constructing a kratos HTTP server.
func ErrorEncoder(w http.ResponseWriter, _ *http.Request, err error) {
	e := Envelope{Data: nullData}
	if se := errors.FromError(err); se != nil {
		e.Code = se.Code
		e.Message = se.Message
		e.Reason = se.Reason
		e.Metadata = se.Metadata
	} else {
		e.Code = int32(http.StatusInternalServerError)
		e.Message = err.Error()
	}
	_ = writeEnvelope(w, e)
}

// writeEnvelope marshals and writes the envelope. Per the unified-envelope
// convention the HTTP status is always 200 and the business result lives in the
// body's code field; change the WriteHeader argument here to surface semantic
// HTTP status codes instead.
func writeEnvelope(w http.ResponseWriter, e Envelope) error {
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, err = w.Write(body)
	return err
}
