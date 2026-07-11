package dbusactivation

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type Client struct {
	conn     *net.UnixConn
	frontend Frontend
	nextID   atomic.Uint64
	mu       sync.Mutex
}

func DialClient(ctx context.Context, path string, frontend Frontend) (*Client, error) {
	if !validFrontend(frontend) {
		return nil, errors.New("invalid frontend")
	}
	dialer := net.Dialer{}
	connection, err := dialer.DialContext(ctx, "unixpacket", path)
	if err != nil {
		return nil, err
	}
	conn, ok := connection.(*net.UnixConn)
	if !ok {
		connection.Close()
		return nil, errors.New("control connection is not Unix")
	}
	client := &Client{conn: conn, frontend: frontend}
	client.nextID.Store(1)
	if err := client.withDeadline(ctx, func() error {
		requestID := client.nextRequestID()
		if err := client.write(Packet{Type: MessageHello, RequestID: requestID, Payload: []byte{byte(frontend)}}); err != nil {
			return err
		}
		packet, err := client.read()
		if err != nil {
			return err
		}
		if packet.Type != MessageHello || packet.RequestID != requestID || len(packet.Payload) != 1 || Frontend(packet.Payload[0]) != frontend {
			return errors.New("invalid hello response")
		}
		return nil
	}); err != nil {
		conn.Close()
		return nil, err
	}
	return client, nil
}

func (c *Client) Close() error { return c.conn.Close() }

func (c *Client) Ping(ctx context.Context) error {
	return c.simple(ctx, MessagePing, nil, MessagePing)
}

func (c *Client) Reload(ctx context.Context) error {
	return c.resultRequest(ctx, MessageReload, nil)
}

func (c *Client) Status(ctx context.Context) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var payload []byte
	err := c.withDeadline(ctx, func() error {
		requestID := c.nextRequestID()
		if err := c.write(Packet{Type: MessageStatus, RequestID: requestID}); err != nil {
			return err
		}
		packet, err := c.read()
		if err != nil {
			return err
		}
		if packet.Type != MessageStatus || packet.RequestID != requestID {
			return errors.New("invalid status response")
		}
		payload = packet.Payload
		return nil
	})
	return payload, err
}

func (c *Client) SetEnvironment(ctx context.Context, values map[string]string) error {
	payload, err := EncodeSetEnvironment(SetEnvironmentRequest{Frontend: c.frontend, Values: values})
	if err != nil {
		return err
	}
	return c.resultRequest(ctx, MessageSetEnvironment, payload)
}

func (c *Client) Activate(ctx context.Context, busName string) (ActivationResult, error) {
	payload, err := EncodeActivate(ActivateRequest{Frontend: c.frontend, BusName: busName})
	if err != nil {
		return ActivationResult{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var result ActivationResult
	err = c.withDeadline(ctx, func() error {
		requestID := c.nextRequestID()
		if err := c.write(Packet{Type: MessageActivate, RequestID: requestID, Payload: payload}); err != nil {
			return err
		}
		packet, err := c.read()
		if err != nil {
			return err
		}
		if packet.Type != MessageActivationResult || packet.RequestID != requestID {
			return errors.New("invalid activation response")
		}
		result, err = DecodeActivationResult(packet.Payload)
		return err
	})
	return result, err
}

func (c *Client) simple(ctx context.Context, messageType MessageType, payload []byte, responseType MessageType) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.withDeadline(ctx, func() error {
		requestID := c.nextRequestID()
		if err := c.write(Packet{Type: messageType, RequestID: requestID, Payload: payload}); err != nil {
			return err
		}
		packet, err := c.read()
		if err != nil {
			return err
		}
		if packet.Type != responseType || packet.RequestID != requestID {
			return errors.New("invalid control response")
		}
		return nil
	})
}

func (c *Client) resultRequest(ctx context.Context, messageType MessageType, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.withDeadline(ctx, func() error {
		requestID := c.nextRequestID()
		if err := c.write(Packet{Type: messageType, RequestID: requestID, Payload: payload}); err != nil {
			return err
		}
		packet, err := c.read()
		if err != nil {
			return err
		}
		if packet.Type != MessageActivationResult || packet.RequestID != requestID {
			return errors.New("invalid result response")
		}
		result, err := DecodeActivationResult(packet.Payload)
		if err != nil {
			return err
		}
		if result.Code != ResultSuccess {
			return errors.New(result.Detail)
		}
		return nil
	})
}

func (c *Client) nextRequestID() uint64 { return c.nextID.Add(1) }

func (c *Client) write(packet Packet) error {
	data, err := EncodePacket(packet)
	if err != nil {
		return err
	}
	_, err = c.conn.Write(data)
	return err
}

func (c *Client) read() (Packet, error) {
	buffer := make([]byte, HeaderSize+int(MaxPayload)+1)
	n, err := c.conn.Read(buffer)
	if err != nil {
		return Packet{}, err
	}
	return DecodePacket(buffer[:n])
}

func (c *Client) withDeadline(ctx context.Context, fn func() error) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Time{}
	}
	if err := c.conn.SetDeadline(deadline); err != nil {
		return err
	}
	err := fn()
	_ = c.conn.SetDeadline(time.Time{})
	return err
}
