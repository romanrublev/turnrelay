package control

import (
	"bufio"
	"errors"
	"net"

	"github.com/romanrublev/turnrelay/app/internal/profile"
	"github.com/romanrublev/turnrelay/app/internal/proto"
)

type Client struct{ Path string }

func (c Client) do(req proto.Request) (proto.Response, error) {
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: c.Path, Net: "unix"})
	if err != nil {
		return proto.Response{}, err
	}
	defer conn.Close()
	if err := proto.WriteMessage(conn, req); err != nil {
		return proto.Response{}, err
	}
	return proto.ReadResponse(bufio.NewReader(conn))
}

func (c Client) Up(p profile.Profile) error {
	resp, err := c.do(proto.Request{Cmd: proto.CmdUp, Profile: &p})
	if err != nil {
		return err
	}
	if !resp.OK {
		return errors.New(resp.Error)
	}
	return nil
}

func (c Client) Down() error {
	resp, err := c.do(proto.Request{Cmd: proto.CmdDown})
	if err != nil {
		return err
	}
	if !resp.OK {
		return errors.New(resp.Error)
	}
	return nil
}

func (c Client) Status() (proto.Status, error) {
	resp, err := c.do(proto.Request{Cmd: proto.CmdStatus})
	if err != nil {
		return proto.Status{}, err
	}
	if !resp.OK {
		return proto.Status{}, errors.New(resp.Error)
	}
	if resp.Status == nil {
		return proto.Status{}, errNotOK
	}
	return *resp.Status, nil
}
