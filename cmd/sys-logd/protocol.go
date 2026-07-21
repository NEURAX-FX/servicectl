package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

const (
	protocolVersion        = 1
	maxProtocolPacketBytes = 1 << 20
)

type packetKind string

const (
	packetHello packetKind = "hello"
	packetLog   packetKind = "log"
	packetAck   packetKind = "ack"
	packetNack  packetKind = "nack"
)

type protocolPacket struct {
	Version   int        `json:"version"`
	Kind      packetKind `json:"kind"`
	Sequence  uint64     `json:"sequence,omitempty"`
	Priority  int        `json:"priority,omitempty"`
	Message   string     `json:"message,omitempty"`
	Retryable bool       `json:"retryable,omitempty"`
	Error     string     `json:"error,omitempty"`
}

func encodeProtocolPacket(packet protocolPacket) ([]byte, error) {
	if err := validateProtocolPacket(packet); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(packet)
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxProtocolPacketBytes {
		return nil, fmt.Errorf("protocol packet is %d bytes, limit is %d", len(encoded), maxProtocolPacketBytes)
	}
	return encoded, nil
}

func decodeProtocolPacket(encoded []byte) (protocolPacket, error) {
	if len(encoded) > maxProtocolPacketBytes {
		return protocolPacket{}, fmt.Errorf("protocol packet is %d bytes, limit is %d", len(encoded), maxProtocolPacketBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var packet protocolPacket
	if err := decoder.Decode(&packet); err != nil {
		return protocolPacket{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return protocolPacket{}, fmt.Errorf("protocol packet contains multiple JSON values")
		}
		return protocolPacket{}, err
	}
	if err := validateProtocolPacket(packet); err != nil {
		return protocolPacket{}, err
	}
	return packet, nil
}

func validateProtocolPacket(packet protocolPacket) error {
	if packet.Version != protocolVersion {
		return fmt.Errorf("unsupported protocol version %d", packet.Version)
	}
	switch packet.Kind {
	case packetHello:
		if packet.Sequence != 0 || packet.Priority != 0 || packet.Message != "" || packet.Retryable || packet.Error != "" {
			return fmt.Errorf("hello packet contains log or response fields")
		}
	case packetLog:
		if packet.Sequence == 0 {
			return fmt.Errorf("log packet sequence is zero")
		}
		if packet.Priority < 0 || packet.Priority > 7 {
			return fmt.Errorf("log packet priority %d is outside 0..7", packet.Priority)
		}
		if packet.Message == "" {
			return fmt.Errorf("log packet message is empty")
		}
		if packet.Retryable || packet.Error != "" {
			return fmt.Errorf("log packet contains response fields")
		}
	case packetAck:
		if packet.Sequence == 0 {
			return fmt.Errorf("ack packet sequence is zero")
		}
		if packet.Priority != 0 || packet.Message != "" || packet.Retryable || packet.Error != "" {
			return fmt.Errorf("ack packet contains log or error fields")
		}
	case packetNack:
		if packet.Priority != 0 || packet.Message != "" || packet.Error == "" {
			return fmt.Errorf("nack packet has an invalid shape")
		}
	default:
		return fmt.Errorf("unsupported packet kind %q", packet.Kind)
	}
	return nil
}
