package protocol

import (
	"encoding/json"
	"errors"
	"time"
)

const ProtocolVersion = "1.0.0"

type ErrorCode string

const (
	ErrInvalidRequest   ErrorCode = "INVALID_REQUEST"
	ErrUnknownCommand   ErrorCode = "UNKNOWN_COMMAND"
	ErrAdapterNotFound  ErrorCode = "ADAPTER_NOT_FOUND"
	ErrInternalError    ErrorCode = "INTERNAL_ERROR"
	ErrUnsupportedOp    ErrorCode = "UNSUPPORTED_OPERATION"
	ErrWintunUnavailable ErrorCode = "WINTUN_UNAVAILABLE"
	ErrAccessDenied     ErrorCode = "ACCESS_DENIED"
	ErrWintunError      ErrorCode = "WINTUN_ERROR"
	ErrPipelineError    ErrorCode = "PIPELINE_ERROR"
	ErrTransportError   ErrorCode = "TRANSPORT_ERROR"
	ErrRelayError       ErrorCode = "RELAY_ERROR"
)

type ErrorInfo struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

type Request struct {
	ProtocolVersion string          `json:"protocol_version"`
	RequestID       string          `json:"request_id"`
	Type            string          `json:"type"`
	Payload         json.RawMessage `json:"payload,omitempty"`
}

type Response struct {
	ProtocolVersion string     `json:"protocol_version"`
	RequestID       string     `json:"request_id"`
	Type            string     `json:"type"`
	Success         bool       `json:"success"`
	Data            any        `json:"data,omitempty"`
	Error           *ErrorInfo `json:"error,omitempty"`
	Timestamp       int64      `json:"timestamp"`
}

type Event struct {
	ProtocolVersion string `json:"protocol_version"`
	Type            string `json:"type"`
	Event           string `json:"event"`
	Data            any    `json:"data"`
	Timestamp       int64  `json:"timestamp"`
}

func NewSuccessResponse(requestID string, data any) Response {
	return Response{
		ProtocolVersion: ProtocolVersion,
		RequestID:       requestID,
		Type:            "response",
		Success:         true,
		Data:            data,
		Timestamp:       time.Now().UnixMilli(),
	}
}

func NewErrorResponse(requestID string, code ErrorCode, msg string) Response {
	return Response{
		ProtocolVersion: ProtocolVersion,
		RequestID:       requestID,
		Type:            "error",
		Success:         false,
		Error: &ErrorInfo{
			Code:    code,
			Message: msg,
		},
		Timestamp: time.Now().UnixMilli(),
	}
}

func NewEvent(eventName string, data any) Event {
	return Event{
		ProtocolVersion: ProtocolVersion,
		Type:            "event",
		Event:           eventName,
		Data:            data,
		Timestamp:       time.Now().UnixMilli(),
	}
}

func ValidateRequest(req *Request) error {
	if req.ProtocolVersion == "" {
		return errors.New("missing protocol_version")
	}
	if req.RequestID == "" {
		return errors.New("missing request_id")
	}
	if req.Type == "" {
		return errors.New("missing type")
	}
	return nil
}

type AssignPathPayload struct {
	PathID    string `json:"path_id"`    // "modem" or "android"
	AdapterID string `json:"adapter_id"` // matching adapter ID
}

type WintunStartSessionPayload struct {
	Capacity uint32 `json:"capacity,omitempty"`
}

type WintunDeleteAdapterPayload struct {
	Confirm bool `json:"confirm"`
}

type WintunSendTestPacketPayload struct {
	PayloadSize int `json:"payload_size,omitempty"`
}

type PacketSetTestModePayload struct {
	Enabled bool `json:"enabled"`
}

type PacketInjectTestPayload struct {
	IPVersion   int  `json:"ip_version,omitempty"` // 4 or 6
	PayloadSize int  `json:"payload_size,omitempty"`
	Corrupt     bool `json:"corrupt,omitempty"`
}

type MultipathSetModePayload struct {
	Mode string `json:"mode"`
}

type MultipathSetWeightsPayload struct {
	Weights map[string]uint32 `json:"weights"`
}

type RelayConnectPayload struct {
	RelayAddr         string `json:"relay_addr"`
	PSKHex            string `json:"psk_hex"`
	RelayStaticPubHex string `json:"relay_static_pub_hex"`
	SOCKS5Listen      string `json:"socks5_listen,omitempty"`
}
