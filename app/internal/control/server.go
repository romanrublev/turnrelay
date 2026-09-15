// Package control is the unix-socket control plane between the CLI/GUI and the
// daemon: a uid-authorized server and a thin client.
package control

import (
	"bufio"
	"errors"
	"net"

	"github.com/romanrublev/turnrelay/app/internal/profile"
	"github.com/romanrublev/turnrelay/app/internal/proto"
)

type Handler interface {
	Up(profile.Profile) error
	Down() error
	Status() proto.Status
}

func Serve(ln *net.UnixListener, ownerUID uint32, h Handler) error {
	for {
		conn, err := ln.AcceptUnix()
		if err != nil {
			return err
		}
		go handleConn(conn, ownerUID, h)
	}
}

func handleConn(conn *net.UnixConn, ownerUID uint32, h Handler) {
	defer conn.Close()
	defer func() {
		if r := recover(); r != nil {
			proto.WriteMessage(conn, proto.Response{OK: false, Error: "internal error"})
		}
	}()
	uid, err := peerUID(conn)
	if err != nil || uid != ownerUID {
		proto.WriteMessage(conn, proto.Response{OK: false, Error: "unauthorized"})
		return
	}
	req, err := proto.ReadRequest(bufio.NewReader(conn))
	if err != nil {
		return
	}
	proto.WriteMessage(conn, dispatch(req, h))
}

func dispatch(req proto.Request, h Handler) proto.Response {
	switch req.Cmd {
	case proto.CmdUp:
		if req.Profile == nil {
			return proto.Response{OK: false, Error: "up: missing profile"}
		}
		if err := h.Up(*req.Profile); err != nil {
			return proto.Response{OK: false, Error: err.Error()}
		}
		return proto.Response{OK: true}
	case proto.CmdDown:
		if err := h.Down(); err != nil {
			return proto.Response{OK: false, Error: err.Error()}
		}
		return proto.Response{OK: true}
	case proto.CmdStatus:
		st := h.Status()
		return proto.Response{OK: true, Status: &st}
	default:
		return proto.Response{OK: false, Error: "unknown command: " + req.Cmd}
	}
}

var errNotOK = errors.New("daemon returned not-ok")
