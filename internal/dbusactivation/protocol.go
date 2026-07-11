package dbusactivation

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	ProtocolVersion uint16 = 1
	HeaderSize             = 28
	MaxPayload      uint32 = 64 << 10
	MaxBusName             = 255
)

var protocolMagic = [8]byte{'S', 'D', 'B', 'U', 'S', 0, 0, 1}

type MessageType uint16

const (
	MessageHello MessageType = iota + 1
	MessageSetEnvironment
	MessageActivate
	MessageActivationResult
	MessageCancel
	MessageReload
	MessageStatus
	MessagePing
)

type Frontend uint8

const (
	FrontendDaemonHelper Frontend = iota + 1
	FrontendAdmin
)

type ResultCode uint16

const (
	ResultSuccess ResultCode = iota
	ResultConfigInvalid
	ResultUnknownService
	ResultPermissionsInvalid
	ResultFileInvalid
	ResultServiceInvalid
	ResultServiceNotFound
	ResultExecFailed
	ResultForkFailed
	ResultChildExited
	ResultChildSignaled
	ResultFailed
	ResultInvalidArguments
	ResultOutOfMemory
	ResultDuplicateService
	ResultInvalidBusName
	ResultUnitNotFound
	ResultUnitMasked
	ResultSetupFailed
	ResultTimeout
	ResultBackendUnavailable
	ResultProtocolError
	ResultVersionMismatch
)

type Packet struct {
	Type      MessageType
	RequestID uint64
	Flags     uint32
	Payload   []byte
}

type ActivateRequest struct {
	Frontend       Frontend
	BusName        string
	environment    []string
	environmentSet bool
}

type ActivationResult struct {
	Code   ResultCode
	Detail string
}

type SetEnvironmentRequest struct {
	Frontend Frontend
	Values   map[string]string
}

func WritePacket(w io.Writer, packet Packet) error {
	if !validMessageType(packet.Type) {
		return fmt.Errorf("unknown message type %d", packet.Type)
	}
	if packet.Flags != 0 {
		return fmt.Errorf("unsupported flags %#x", packet.Flags)
	}
	if len(packet.Payload) > int(MaxPayload) {
		return fmt.Errorf("payload is %d bytes, maximum is %d", len(packet.Payload), MaxPayload)
	}

	header := make([]byte, HeaderSize)
	copy(header[:8], protocolMagic[:])
	binary.BigEndian.PutUint16(header[8:10], ProtocolVersion)
	binary.BigEndian.PutUint16(header[10:12], uint16(packet.Type))
	binary.BigEndian.PutUint64(header[12:20], packet.RequestID)
	binary.BigEndian.PutUint32(header[20:24], uint32(len(packet.Payload)))
	binary.BigEndian.PutUint32(header[24:28], packet.Flags)
	if _, err := w.Write(header); err != nil {
		return err
	}
	if len(packet.Payload) == 0 {
		return nil
	}
	_, err := w.Write(packet.Payload)
	return err
}

func ReadPacket(r io.Reader) (Packet, error) {
	data, err := io.ReadAll(io.LimitReader(r, int64(HeaderSize)+int64(MaxPayload)+1))
	if err != nil {
		return Packet{}, err
	}
	return DecodePacket(data)
}

func EncodePacket(packet Packet) ([]byte, error) {
	var buf bytes.Buffer
	if err := WritePacket(&buf, packet); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func DecodePacket(data []byte) (Packet, error) {
	if len(data) < HeaderSize {
		return Packet{}, io.ErrUnexpectedEOF
	}
	if !bytes.Equal(data[:8], protocolMagic[:]) {
		return Packet{}, errors.New("invalid protocol magic")
	}
	if version := binary.BigEndian.Uint16(data[8:10]); version != ProtocolVersion {
		return Packet{}, fmt.Errorf("unsupported protocol version %d", version)
	}
	messageType := MessageType(binary.BigEndian.Uint16(data[10:12]))
	if !validMessageType(messageType) {
		return Packet{}, fmt.Errorf("unknown message type %d", messageType)
	}
	flags := binary.BigEndian.Uint32(data[24:28])
	if flags != 0 {
		return Packet{}, fmt.Errorf("unsupported flags %#x", flags)
	}
	payloadLength := binary.BigEndian.Uint32(data[20:24])
	if payloadLength > MaxPayload {
		return Packet{}, fmt.Errorf("payload length %d exceeds maximum %d", payloadLength, MaxPayload)
	}
	if len(data) != HeaderSize+int(payloadLength) {
		return Packet{}, fmt.Errorf("packet length %d does not match payload length %d", len(data), payloadLength)
	}
	payload := append([]byte(nil), data[HeaderSize:]...)
	return Packet{
		Type:      messageType,
		RequestID: binary.BigEndian.Uint64(data[12:20]),
		Flags:     flags,
		Payload:   payload,
	}, nil
}

func EncodeActivate(request ActivateRequest) ([]byte, error) {
	if !validFrontend(request.Frontend) {
		return nil, fmt.Errorf("invalid frontend %d", request.Frontend)
	}
	if err := ValidateBusName(request.BusName); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.WriteByte(byte(request.Frontend))
	if err := writeString16(&buf, request.BusName); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func DecodeActivate(payload []byte) (ActivateRequest, error) {
	reader := bytes.NewReader(payload)
	frontend, err := reader.ReadByte()
	if err != nil {
		return ActivateRequest{}, err
	}
	request := ActivateRequest{Frontend: Frontend(frontend)}
	if !validFrontend(request.Frontend) {
		return ActivateRequest{}, fmt.Errorf("invalid frontend %d", request.Frontend)
	}
	request.BusName, err = readString16(reader, MaxBusName)
	if err != nil {
		return ActivateRequest{}, err
	}
	if reader.Len() != 0 {
		return ActivateRequest{}, errors.New("activate payload has trailing bytes")
	}
	if err := ValidateBusName(request.BusName); err != nil {
		return ActivateRequest{}, err
	}
	return request, nil
}

func EncodeActivationResult(result ActivationResult) ([]byte, error) {
	if !validResultCode(result.Code) {
		return nil, fmt.Errorf("invalid result code %d", result.Code)
	}
	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.BigEndian, uint16(result.Code)); err != nil {
		return nil, err
	}
	if err := writeString16(&buf, result.Detail); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func DecodeActivationResult(payload []byte) (ActivationResult, error) {
	reader := bytes.NewReader(payload)
	var rawCode uint16
	if err := binary.Read(reader, binary.BigEndian, &rawCode); err != nil {
		return ActivationResult{}, err
	}
	result := ActivationResult{Code: ResultCode(rawCode)}
	if !validResultCode(result.Code) {
		return ActivationResult{}, fmt.Errorf("invalid result code %d", result.Code)
	}
	detail, err := readString16(reader, int(MaxPayload))
	if err != nil {
		return ActivationResult{}, err
	}
	if reader.Len() != 0 {
		return ActivationResult{}, errors.New("activation result has trailing bytes")
	}
	result.Detail = detail
	return result, nil
}

func EncodeSetEnvironment(request SetEnvironmentRequest) ([]byte, error) {
	if !validFrontend(request.Frontend) {
		return nil, fmt.Errorf("invalid frontend %d", request.Frontend)
	}
	if len(request.Values) > 256 {
		return nil, errors.New("environment has too many entries")
	}
	keys := make([]string, 0, len(request.Values))
	for key := range request.Values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var buf bytes.Buffer
	buf.WriteByte(byte(request.Frontend))
	if err := binary.Write(&buf, binary.BigEndian, uint16(len(keys))); err != nil {
		return nil, err
	}
	for _, key := range keys {
		value := request.Values[key]
		if err := validateEnvironmentEntry(key, value); err != nil {
			return nil, err
		}
		if err := writeString16(&buf, key); err != nil {
			return nil, err
		}
		if err := writeString32(&buf, value); err != nil {
			return nil, err
		}
	}
	if buf.Len() > int(MaxPayload) {
		return nil, errors.New("environment payload is too large")
	}
	return buf.Bytes(), nil
}

func DecodeSetEnvironment(payload []byte) (SetEnvironmentRequest, error) {
	reader := bytes.NewReader(payload)
	frontend, err := reader.ReadByte()
	if err != nil {
		return SetEnvironmentRequest{}, err
	}
	request := SetEnvironmentRequest{Frontend: Frontend(frontend)}
	if !validFrontend(request.Frontend) {
		return SetEnvironmentRequest{}, fmt.Errorf("invalid frontend %d", request.Frontend)
	}
	var count uint16
	if err := binary.Read(reader, binary.BigEndian, &count); err != nil {
		return SetEnvironmentRequest{}, err
	}
	if count > 256 {
		return SetEnvironmentRequest{}, errors.New("environment has too many entries")
	}
	request.Values = make(map[string]string, int(count))
	for i := 0; i < int(count); i++ {
		key, err := readString16(reader, 4096)
		if err != nil {
			return SetEnvironmentRequest{}, err
		}
		value, err := readString32(reader, 8192)
		if err != nil {
			return SetEnvironmentRequest{}, err
		}
		if err := validateEnvironmentEntry(key, value); err != nil {
			return SetEnvironmentRequest{}, err
		}
		if _, exists := request.Values[key]; exists {
			return SetEnvironmentRequest{}, fmt.Errorf("duplicate environment variable %q", key)
		}
		request.Values[key] = value
	}
	if reader.Len() != 0 {
		return SetEnvironmentRequest{}, errors.New("environment payload has trailing bytes")
	}
	return request, nil
}

func ValidateBusName(name string) error {
	if name == "" || len(name) > MaxBusName || !utf8.ValidString(name) || strings.HasPrefix(name, ":") || !strings.Contains(name, ".") {
		return fmt.Errorf("invalid well-known bus name %q", name)
	}
	for _, part := range strings.Split(name, ".") {
		if part == "" || !validBusElementStart(part[0]) {
			return fmt.Errorf("invalid well-known bus name %q", name)
		}
		for i := 1; i < len(part); i++ {
			if !validBusElement(part[i]) {
				return fmt.Errorf("invalid well-known bus name %q", name)
			}
		}
	}
	return nil
}

func validMessageType(messageType MessageType) bool {
	return messageType >= MessageHello && messageType <= MessagePing
}

func validFrontend(frontend Frontend) bool {
	return frontend == FrontendDaemonHelper || frontend == FrontendAdmin
}

func validResultCode(code ResultCode) bool {
	return code <= ResultVersionMismatch
}

func validBusElementStart(ch byte) bool {
	return ch == '_' || ch == '-' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z'
}

func validBusElement(ch byte) bool {
	return validBusElementStart(ch) || ch >= '0' && ch <= '9'
}

func validateEnvironmentEntry(key, value string) error {
	if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, '\x00') || !utf8.ValidString(key) || !utf8.ValidString(value) {
		return fmt.Errorf("invalid environment entry %q", key)
	}
	return nil
}

func writeString16(buf *bytes.Buffer, value string) error {
	if !utf8.ValidString(value) || len(value) > int(^uint16(0)) {
		return errors.New("string is invalid or too long")
	}
	if err := binary.Write(buf, binary.BigEndian, uint16(len(value))); err != nil {
		return err
	}
	_, err := buf.WriteString(value)
	return err
}

func writeString32(buf *bytes.Buffer, value string) error {
	if !utf8.ValidString(value) || uint64(len(value)) > uint64(^uint32(0)) {
		return errors.New("string is invalid or too long")
	}
	if err := binary.Write(buf, binary.BigEndian, uint32(len(value))); err != nil {
		return err
	}
	_, err := buf.WriteString(value)
	return err
}

func readString16(reader *bytes.Reader, maximum int) (string, error) {
	var length uint16
	if err := binary.Read(reader, binary.BigEndian, &length); err != nil {
		return "", err
	}
	return readString(reader, int(length), maximum)
}

func readString32(reader *bytes.Reader, maximum int) (string, error) {
	var length uint32
	if err := binary.Read(reader, binary.BigEndian, &length); err != nil {
		return "", err
	}
	if uint64(length) > uint64(maximum) {
		return "", errors.New("string exceeds maximum length")
	}
	return readString(reader, int(length), maximum)
}

func readString(reader *bytes.Reader, length, maximum int) (string, error) {
	if length > maximum || length > reader.Len() {
		return "", errors.New("string exceeds available or allowed length")
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(reader, data); err != nil {
		return "", err
	}
	if !utf8.Valid(data) {
		return "", errors.New("string is not valid UTF-8")
	}
	return string(data), nil
}
