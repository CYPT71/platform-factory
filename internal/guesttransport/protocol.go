// Package guesttransport implements the small authenticated control protocol
// shared by native VMM backends and the in-guest agent.
package guesttransport

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

const (
	Version      = 1
	MaxFrameSize = 1 << 20
	MaxArguments = 128
)

type Operation string

const (
	OpExec     Operation = "exec"
	OpSignal   Operation = "signal"
	OpShutdown Operation = "shutdown"
	OpState    Operation = "state"
	OpLogs     Operation = "logs"
)

type Request struct {
	Operation Operation `json:"operation"`
	Args      []string  `json:"args,omitempty"`
	Signal    string    `json:"signal,omitempty"`
	Stdin     []byte    `json:"stdin,omitempty"`
}

type Response struct {
	ExitCode int    `json:"exit_code,omitempty"`
	State    string `json:"state,omitempty"`
	Stdout   []byte `json:"stdout,omitempty"`
	Stderr   []byte `json:"stderr,omitempty"`
	Logs     []byte `json:"logs,omitempty"`
	Error    string `json:"error,omitempty"`
}

type envelope struct {
	Version int             `json:"version"`
	Seq     uint64          `json:"seq"`
	Payload json.RawMessage `json:"payload"`
	MAC     string          `json:"mac"`
}

// Codec frames messages with a 32-bit length and authenticates their version,
// sequence number and payload with HMAC-SHA256. A Codec is directional:
// receive sequence numbers must be strictly increasing, rejecting replay.
type Codec struct {
	r   *bufio.Reader
	w   io.Writer
	key []byte

	readMu  sync.Mutex
	writeMu sync.Mutex
	recvSeq uint64
}

func NewCodec(r io.Reader, w io.Writer, key []byte) (*Codec, error) {
	if len(key) < 32 {
		return nil, errors.New("guest transport: session key must contain at least 32 bytes")
	}
	return &Codec{r: bufio.NewReader(r), w: w, key: append([]byte(nil), key...)}, nil
}

func (c *Codec) Write(seq uint64, value any) error {
	if seq == 0 {
		return errors.New("guest transport: sequence number must be non-zero")
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("guest transport: encode payload: %w", err)
	}
	env := envelope{Version: Version, Seq: seq, Payload: payload}
	env.MAC = c.mac(env.Version, env.Seq, env.Payload)
	frame, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("guest transport: encode envelope: %w", err)
	}
	if len(frame) > MaxFrameSize {
		return errors.New("guest transport: frame exceeds 1 MiB")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(frame)))
	if _, err := c.w.Write(header[:]); err != nil {
		return fmt.Errorf("guest transport: write header: %w", err)
	}
	if _, err := c.w.Write(frame); err != nil {
		return fmt.Errorf("guest transport: write frame: %w", err)
	}
	return nil
}

func (c *Codec) Read(dst any) (uint64, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	var header [4]byte
	if _, err := io.ReadFull(c.r, header[:]); err != nil {
		return 0, fmt.Errorf("guest transport: read header: %w", err)
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > MaxFrameSize {
		return 0, errors.New("guest transport: invalid frame size")
	}
	frame := make([]byte, size)
	if _, err := io.ReadFull(c.r, frame); err != nil {
		return 0, fmt.Errorf("guest transport: read frame: %w", err)
	}
	var env envelope
	decoder := json.NewDecoder(bytes.NewReader(frame))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&env); err != nil {
		return 0, fmt.Errorf("guest transport: decode envelope: %w", err)
	}
	if env.Version != Version {
		return 0, fmt.Errorf("guest transport: unsupported version %d", env.Version)
	}
	if env.Seq == 0 || env.Seq <= c.recvSeq {
		return 0, errors.New("guest transport: replayed or unordered sequence")
	}
	expected, err := hex.DecodeString(env.MAC)
	actual, _ := hex.DecodeString(c.mac(env.Version, env.Seq, env.Payload))
	if err != nil || !hmac.Equal(expected, actual) {
		return 0, errors.New("guest transport: authentication failed")
	}
	if err := json.Unmarshal(env.Payload, dst); err != nil {
		return 0, fmt.Errorf("guest transport: decode payload: %w", err)
	}
	c.recvSeq = env.Seq
	return env.Seq, nil
}

func (c *Codec) mac(version int, seq uint64, payload []byte) string {
	h := hmac.New(sha256.New, c.key)
	var prefix [12]byte
	binary.BigEndian.PutUint32(prefix[:4], uint32(version))
	binary.BigEndian.PutUint64(prefix[4:], seq)
	_, _ = h.Write(prefix[:])
	_, _ = h.Write(payload)
	return hex.EncodeToString(h.Sum(nil))
}

type Handler interface {
	Handle(context.Context, Request) Response
}

// ServeOne validates one request and writes exactly one correlated response.
func ServeOne(ctx context.Context, codec *Codec, handler Handler) error {
	var request Request
	seq, err := codec.Read(&request)
	if err != nil {
		return err
	}
	if err := validateRequest(request); err != nil {
		return codec.Write(seq, Response{Error: err.Error()})
	}
	return codec.Write(seq, handler.Handle(ctx, request))
}

func validateRequest(request Request) error {
	switch request.Operation {
	case OpExec:
		if len(request.Args) == 0 {
			return errors.New("exec requires an argument")
		}
		if len(request.Args) > MaxArguments {
			return errors.New("exec has too many arguments")
		}
	case OpSignal:
		if request.Signal == "" {
			return errors.New("signal is required")
		}
	case OpShutdown, OpState, OpLogs:
	default:
		return fmt.Errorf("unsupported operation %q", request.Operation)
	}
	return nil
}

// Client serializes exchanges so sequence correlation remains deterministic
// even when several callers share one virtio socket.
type Client struct {
	codec *Codec
	mu    sync.Mutex
	seq   uint64
}

func NewClient(codec *Codec) *Client { return &Client{codec: codec} }

func (c *Client) Do(ctx context.Context, request Request) (Response, error) {
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	if err := c.codec.Write(c.seq, request); err != nil {
		return Response{}, err
	}
	var response Response
	seq, err := c.codec.Read(&response)
	if err != nil {
		return Response{}, err
	}
	if seq != c.seq {
		return Response{}, errors.New("guest transport: response sequence mismatch")
	}
	if response.Error != "" {
		return response, errors.New(response.Error)
	}
	return response, nil
}
