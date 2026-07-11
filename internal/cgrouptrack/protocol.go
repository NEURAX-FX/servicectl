package cgrouptrack

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	MaxRequestBytes  = 64 * 1024
	MaxResponseBytes = 64 * 1024
)

type Operation string

const (
	OpStatus    Operation = "status"
	OpListUnits Operation = "list-units"
	OpGetUnit   Operation = "get-unit"
	OpListPIDs  Operation = "list-pids"
	OpAttach    Operation = "attach"
)

type Request struct {
	Operation Operation `json:"operation"`
	Mode      Mode      `json:"mode,omitempty"`
	UID       uint32    `json:"uid,omitempty"`
	Unit      string    `json:"unit,omitempty"`
	PID       int       `json:"pid,omitempty"`
}

func (r Request) Validate() error {
	switch r.Operation {
	case OpStatus, OpListUnits:
		if r.Unit != "" || r.PID != 0 {
			return errors.New("operation does not accept unit or pid")
		}
		return validateScopeFields(r.Mode, r.UID)
	case OpGetUnit, OpListPIDs:
		if r.PID != 0 {
			return errors.New("operation does not accept pid")
		}
		return validateUnitFields(r.Mode, r.UID, r.Unit)
	case OpAttach:
		if r.PID <= 0 {
			return errors.New("attach pid must be positive")
		}
		return validateUnitFields(r.Mode, r.UID, r.Unit)
	default:
		return fmt.Errorf("unknown operation %q", r.Operation)
	}
}

func validateScopeFields(mode Mode, uid uint32) error {
	if mode == "" {
		if uid != 0 {
			return errors.New("global scope UID must be zero")
		}
		return nil
	}
	return (UnitKey{Mode: mode, UID: uid, Unit: "scope.service"}).Validate()
}

func validateUnitFields(mode Mode, uid uint32, unit string) error {
	if mode == "" {
		return errors.New("unit operation requires a mode")
	}
	return (UnitKey{Mode: mode, UID: uid, Unit: unit}).Validate()
}

type Response struct {
	OK     bool            `json:"ok"`
	Error  *APIError       `json:"error,omitempty"`
	Status *DaemonStatus   `json:"status,omitempty"`
	Units  []UnitStatus    `json:"units,omitempty"`
	Unit   *UnitStatus     `json:"unit,omitempty"`
	PIDs   []ProcessStatus `json:"pids,omitempty"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

type Service interface {
	Status(context.Context, Scope) (DaemonStatus, error)
	ListUnits(context.Context, Scope) ([]UnitStatus, error)
	GetUnit(context.Context, Scope, string) (UnitStatus, error)
	ListPIDs(context.Context, Scope, string) ([]ProcessStatus, error)
	Attach(context.Context, Scope, string, int) (UnitStatus, error)
}

func EncodeRequest(w io.Writer, request Request) error {
	if err := request.Validate(); err != nil {
		return err
	}
	return encodeFrame(w, request, MaxRequestBytes)
}

func DecodeRequest(r io.Reader) (Request, error) {
	var request Request
	if err := decodeFrame(r, &request, MaxRequestBytes); err != nil {
		return Request{}, err
	}
	if err := request.Validate(); err != nil {
		return Request{}, err
	}
	return request, nil
}

func EncodeResponse(w io.Writer, response Response) error {
	return encodeFrame(w, response, MaxResponseBytes)
}

func DecodeResponse(r io.Reader) (Response, error) {
	var response Response
	if err := decodeFrame(r, &response, MaxResponseBytes); err != nil {
		return Response{}, err
	}
	return response, nil
}

func encodeFrame(w io.Writer, value any, maximum uint32) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(body) > int(maximum) {
		return fmt.Errorf("JSON frame exceeds %d bytes", maximum)
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(body)))
	if err := writeAll(w, header[:]); err != nil {
		return err
	}
	return writeAll(w, body)
}

func decodeFrame(r io.Reader, value any, maximum uint32) error {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 {
		return errors.New("empty JSON frame")
	}
	if size > maximum {
		return fmt.Errorf("JSON frame exceeds %d bytes", maximum)
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(r, body); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON data")
		}
		return err
	}
	return nil
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) != 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
