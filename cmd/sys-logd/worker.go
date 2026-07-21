package main

import (
	"fmt"
	"net"
	"sync"
	"time"
)

type messageSink interface {
	WriteMessage(string) error
	Close() error
}

type brokerNackError struct {
	Retryable bool
	Message   string
}

func (err *brokerNackError) Error() string {
	return err.Message
}

type brokerSink struct {
	mu      sync.Mutex
	path    string
	timeout time.Duration
	conn    *net.UnixConn
	seq     uint64
}

func newBrokerSink(path string, timeout time.Duration) *brokerSink {
	if timeout <= 0 {
		timeout = brokerPacketDeadline
	}
	return &brokerSink{path: path, timeout: timeout}
}

func (sink *brokerSink) WriteMessage(message string) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if err := sink.ensureConnectedLocked(); err != nil {
		return err
	}
	sink.seq++
	if sink.seq == 0 {
		sink.seq++
	}
	packet := protocolPacket{
		Version:  protocolVersion,
		Kind:     packetLog,
		Sequence: sink.seq,
		Priority: 6,
		Message:  message,
	}
	if err := sink.writePacketLocked(packet); err != nil {
		sink.closeLocked()
		return err
	}
	response, err := sink.readPacketLocked()
	if err != nil {
		sink.closeLocked()
		return err
	}
	if response.Kind == packetNack {
		sink.closeLocked()
		return &brokerNackError{Retryable: response.Retryable, Message: response.Error}
	}
	if response.Kind != packetAck || response.Sequence != packet.Sequence {
		sink.closeLocked()
		return fmt.Errorf("unexpected broker response %#v", response)
	}
	return nil
}

func (sink *brokerSink) ensureConnectedLocked() error {
	if sink.conn != nil {
		return nil
	}
	conn, err := net.DialUnix("unixpacket", nil, &net.UnixAddr{Name: sink.path, Net: "unixpacket"})
	if err != nil {
		return err
	}
	sink.conn = conn
	if err := sink.writePacketLocked(protocolPacket{Version: protocolVersion, Kind: packetHello}); err != nil {
		sink.closeLocked()
		return err
	}
	return nil
}

func (sink *brokerSink) writePacketLocked(packet protocolPacket) error {
	encoded, err := encodeProtocolPacket(packet)
	if err != nil {
		return err
	}
	if err := sink.conn.SetWriteDeadline(time.Now().Add(sink.timeout)); err != nil {
		return err
	}
	written, err := sink.conn.Write(encoded)
	if err != nil {
		return err
	}
	if written != len(encoded) {
		return fmt.Errorf("short broker write: %d of %d bytes", written, len(encoded))
	}
	return nil
}

func (sink *brokerSink) readPacketLocked() (protocolPacket, error) {
	if err := sink.conn.SetReadDeadline(time.Now().Add(sink.timeout)); err != nil {
		return protocolPacket{}, err
	}
	buffer := make([]byte, maxProtocolPacketBytes+1)
	n, err := sink.conn.Read(buffer)
	if err != nil {
		return protocolPacket{}, err
	}
	return decodeProtocolPacket(buffer[:n])
}

func (sink *brokerSink) Close() error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return sink.closeLocked()
}

func (sink *brokerSink) closeLocked() error {
	if sink.conn == nil {
		return nil
	}
	err := sink.conn.Close()
	sink.conn = nil
	return err
}
