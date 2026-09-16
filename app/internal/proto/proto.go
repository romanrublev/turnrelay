// Package proto is the line-delimited JSON control protocol between the
// turnrelay CLI/GUI and the daemon.
package proto

import (
	"bufio"
	"encoding/json"
	"io"

	"github.com/romanrublev/turnrelay/app/internal/profile"
)

const (
	CmdUp     = "up"
	CmdDown   = "down"
	CmdStatus = "status"
)

type Request struct {
	Cmd     string           `json:"cmd"`
	Profile *profile.Profile `json:"profile,omitempty"`
}

type Status struct {
	Running     bool   `json:"running"`
	Workers     int    `json:"workers"`
	Egress      string `json:"egress"`
	HandshakeOK bool   `json:"handshake_ok"`
	Error       string `json:"error"`
	// Health-based worker pool telemetry.
	Evictions  int     `json:"evictions"`    // cumulative lossy relays retired
	MaxLossPct float64 `json:"max_loss_pct"` // worst active worker's smoothed loss
	MeanRTTMs  int     `json:"mean_rtt_ms"`  // mean active-worker relay RTT
}

type Response struct {
	OK     bool    `json:"ok"`
	Error  string  `json:"error,omitempty"`
	Status *Status `json:"status,omitempty"`
}

func WriteMessage(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}

func ReadRequest(r *bufio.Reader) (Request, error) {
	line, err := r.ReadBytes('\n')
	if err != nil {
		return Request{}, err
	}
	var req Request
	return req, json.Unmarshal(line, &req)
}

func ReadResponse(r *bufio.Reader) (Response, error) {
	line, err := r.ReadBytes('\n')
	if err != nil {
		return Response{}, err
	}
	var resp Response
	return resp, json.Unmarshal(line, &resp)
}
