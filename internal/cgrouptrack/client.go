package cgrouptrack

import (
	"context"
	"errors"
	"net"
	"time"
)

type Client struct {
	path string
}

func NewClient(path string) *Client {
	return &Client{path: path}
}

func (c *Client) Do(ctx context.Context, request Request) (Response, error) {
	if err := request.Validate(); err != nil {
		return Response{}, err
	}
	if c == nil || c.path == "" {
		return Response{}, errors.New("cgroup daemon socket path is required")
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", c.path)
	if err != nil {
		return Response{}, err
	}
	defer connection.Close()
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(5 * time.Second)
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return Response{}, err
	}
	if err := EncodeRequest(connection, request); err != nil {
		return Response{}, err
	}
	response, err := DecodeResponse(connection)
	if err != nil {
		return Response{}, err
	}
	if err := responseError(response); err != nil {
		return response, err
	}
	return response, nil
}

func responseError(response Response) error {
	if response.OK {
		return nil
	}
	if response.Error != nil {
		return response.Error
	}
	return errors.New("cgroup daemon request failed")
}
