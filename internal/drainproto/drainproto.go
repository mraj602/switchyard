// Package drainproto is the wire format of `wy outbox drain --stdio`, the protocol the hub
// speaks over an SSH channel to pull a machine's outbox (docs/spec/machines.md 3.5, proto 1).
//
// One frame is one line of compact UTF-8 JSON ending in "\n", at most MaxLine bytes, with a
// type field `t`. Records travel embedded raw: a Batch holds each outbox line as the exact
// bytes the outbox stores, and neither Writer nor Reader decodes and re-encodes them, so the
// hub stores byte for byte what the machine wrote. Unknown fields are ignored on both sides;
// an unknown `t` is a protocol error.
//
// This is a leaf package: it imports nothing from internal/, so the machine side
// (internal/client) and the hub side (internal/hub/pull) both import it without a cycle.
// It knows frames, limits and timeouts, not outboxes, hubs or ingest.
package drainproto

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"
)

// Proto is the protocol version carried by hello and start (machines.md 3.5).
const Proto = 1

// Limits and timeouts of machines.md 3.5. The hub's start frame may lower MaxRecords,
// MaxBytes and the budget for one session; it never raises them.
const (
	// MaxRecords is the most records in one batch (the same limit as an HTTP upload).
	MaxRecords = 500
	// MaxBytes is the most record bytes in one batch.
	MaxBytes = 4 << 20
	// MaxLine is the longest frame, newline included.
	MaxLine = 9 << 20
	// Budget is the longest a hub keeps one session open (start.budget_s), and
	// MaxBatches the most batches in one session; either ends it with bye / done "budget".
	Budget     = 30 * time.Second
	MaxBatches = 20
	// HelloTimeout is how long the hub waits for hello (or busy) after the ssh session opens.
	HelloTimeout = 10 * time.Second
	// StartTimeout is how long the remote waits for start or refuse after sending hello.
	StartTimeout = 30 * time.Second
	// AckTimeout is how long the remote waits for ack or nack for the outstanding batch.
	AckTimeout = 60 * time.Second
	// BusyRetry is when the hub retries once after busy; MoreDelay is the wait before the
	// next session after a run ended with `more`.
	BusyRetry = 5 * time.Second
	MoreDelay = 1 * time.Second
	// StderrLogBytes is how much of the remote's stderr the hub logs.
	StderrLogBytes = 4096
)

// Frame types (the `t` field).
const (
	TBusy   = "busy"
	THello  = "hello"
	TStart  = "start"
	TRefuse = "refuse"
	TBatch  = "batch"
	TAck    = "ack"
	TNack   = "nack"
	TDone   = "done"
	TBye    = "bye"
)

// Refuse codes (Refuse.Code).
const (
	RefuseProto           = "proto"
	RefuseMachineMismatch = "machine_mismatch"
	RefuseDisabled        = "disabled"
	RefuseFrozen          = "frozen"
)

// Nack codes (Nack.Code).
const NackUnavailable = "unavailable"

// Done reasons (Done.Reason).
const (
	DoneDrained = "drained"
	DoneBudget  = "budget"
	DoneHeld    = "held"
	DoneError   = "error"
)

// Errors of the framing layer. Both ends treat ErrProtocol and its wrappers as a protocol
// error (exit 3 on the remote); io.EOF between frames is a clean end of the stream.
var (
	ErrProtocol     = errors.New("drain: protocol error")
	ErrUnknownFrame = fmt.Errorf("%w: unknown frame type", ErrProtocol)
	ErrFrameTooLong = fmt.Errorf("%w: frame over %d bytes", ErrProtocol, MaxLine)
)

// Frame is one protocol frame. Implemented by the frame structs below (value receivers, so a
// value or a pointer both work with Writer.Write); Reader.Read returns pointers.
type Frame interface {
	// Type is the frame's `t`.
	Type() string
	// Validate checks required fields and ranges. Reader calls it after decoding; Writer
	// before encoding.
	Validate() error
}

// Busy: the remote could not take flush.lock (a local `wy flush` runs). Exit 0.
type Busy struct{}

// Hello is the remote's first frame.
type Hello struct {
	Proto   int     `json:"proto"`
	Machine string  `json:"machine"`
	Name    string  `json:"name"`
	Wy      string  `json:"wy"`       // wy version
	OS      string  `json:"os"`       // GOOS
	Arch    string  `json:"arch"`     // GOARCH
	RecordV int     `json:"record_v"` // highest record v this wy writes
	Backlog int64   `json:"backlog"`  // records past the cursor, an estimate
	Oldest  *string `json:"oldest"`   // RFC 3339 ts of the oldest unanswered record, or null
}

// Start is the hub's answer to hello.
type Start struct {
	Proto      int    `json:"proto"`
	Hub        string `json:"hub"` // hub version
	RecordVMax int    `json:"record_v_max"`
	MaxRecords int    `json:"max_records"` // at most MaxRecords
	MaxBytes   int    `json:"max_bytes"`   // at most MaxBytes
	BudgetS    int    `json:"budget_s"`    // at most 30
}

// Refuse is the hub's other possible answer to hello.
type Refuse struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// Batch carries 1..MaxRecords raw outbox records. Records are the exact stored bytes, one
// JSON object each with no newline inside. More says records remain past this batch.
type Batch struct {
	Seq     int               `json:"seq"`
	Records []json.RawMessage `json:"records"`
	More    bool              `json:"more"`
}

// Rejected is one record the hub refused for good (the IngestReply shape).
type Rejected struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

// Ack answers one batch after the hub committed it: Seq plus exactly the IngestReply fields.
type Ack struct {
	Seq        int        `json:"seq"`
	Accepted   []string   `json:"accepted"`
	Duplicates []string   `json:"duplicates"`
	Rejected   []Rejected `json:"rejected"`
}

// Nack: the hub could not store the batch; nothing is applied, the session ends.
type Nack struct {
	Seq         int    `json:"seq"`
	Code        string `json:"code"`
	RetryAfterS int    `json:"retry_after_s,omitempty"`
}

// Done is the remote's last frame.
type Done struct {
	Reason  string `json:"reason"`
	Backlog int64  `json:"backlog"`
	Held    int64  `json:"held"` // records held back because the hub is too old
}

// Bye: the hub ends the session (its budget, a shutdown).
type Bye struct{}

// Type implements Frame.
func (Busy) Type() string  { return TBusy }
func (Hello) Type() string { return THello }

func (Start) Type() string  { return TStart }
func (Refuse) Type() string { return TRefuse }
func (Batch) Type() string  { return TBatch }
func (Ack) Type() string    { return TAck }
func (Nack) Type() string   { return TNack }
func (Done) Type() string   { return TDone }
func (Bye) Type() string    { return TBye }

func bad(t, format string, a ...any) error {
	return fmt.Errorf("%w: %s: %s", ErrProtocol, t, fmt.Sprintf(format, a...))
}

// Validate implements Frame.
func (Busy) Validate() error { return nil }

// Validate implements Frame.
func (h Hello) Validate() error {
	switch {
	case h.Proto < 1:
		return bad(THello, "proto %d", h.Proto)
	case h.Machine == "":
		return bad(THello, "machine is required")
	case h.Backlog < 0:
		return bad(THello, "negative backlog")
	}
	return nil
}

// Validate implements Frame.
func (s Start) Validate() error {
	switch {
	case s.Proto < 1:
		return bad(TStart, "proto %d", s.Proto)
	case s.MaxRecords < 1 || s.MaxRecords > MaxRecords:
		return bad(TStart, "max_records %d outside 1..%d", s.MaxRecords, MaxRecords)
	case s.MaxBytes < 1 || s.MaxBytes > MaxBytes:
		return bad(TStart, "max_bytes %d outside 1..%d", s.MaxBytes, MaxBytes)
	case s.BudgetS < 1 || s.BudgetS > int(Budget/time.Second):
		return bad(TStart, "budget_s %d outside 1..%d", s.BudgetS, int(Budget/time.Second))
	case s.RecordVMax < 1:
		return bad(TStart, "record_v_max %d", s.RecordVMax)
	}
	return nil
}

// Validate implements Frame.
func (r Refuse) Validate() error {
	switch r.Code {
	case RefuseProto, RefuseMachineMismatch, RefuseDisabled, RefuseFrozen:
		return nil
	}
	return bad(TRefuse, "unknown code %q", r.Code)
}

// Validate implements Frame. It checks the frame's own shape (limits are the receiver's
// start values); every record must be a one-line JSON object.
func (b Batch) Validate() error {
	if b.Seq < 1 {
		return bad(TBatch, "seq %d", b.Seq)
	}
	if len(b.Records) < 1 || len(b.Records) > MaxRecords {
		return bad(TBatch, "%d records outside 1..%d", len(b.Records), MaxRecords)
	}
	total := 0
	for i, r := range b.Records {
		t := bytes.TrimSpace(r)
		if len(t) < 2 || t[0] != '{' || t[len(t)-1] != '}' {
			return bad(TBatch, "record %d is not a JSON object", i)
		}
		if bytes.IndexByte(r, '\n') >= 0 {
			return bad(TBatch, "record %d contains a newline", i)
		}
		total += len(r)
	}
	if total > MaxBytes {
		return bad(TBatch, "%d record bytes over %d", total, MaxBytes)
	}
	return nil
}

// RecordBytes is the total size of the embedded records.
func (b Batch) RecordBytes() int {
	n := 0
	for _, r := range b.Records {
		n += len(r)
	}
	return n
}

// Validate implements Frame.
func (a Ack) Validate() error {
	if a.Seq < 1 {
		return bad(TAck, "seq %d", a.Seq)
	}
	return nil
}

// IDs returns every id the ack names (accepted, duplicates, rejected) in that order.
func (a Ack) IDs() []string {
	out := make([]string, 0, len(a.Accepted)+len(a.Duplicates)+len(a.Rejected))
	out = append(out, a.Accepted...)
	out = append(out, a.Duplicates...)
	for _, r := range a.Rejected {
		out = append(out, r.ID)
	}
	return out
}

// Validate implements Frame.
func (n Nack) Validate() error {
	if n.Seq < 0 || n.Code == "" || n.RetryAfterS < 0 {
		return bad(TNack, "seq %d code %q retry_after_s %d", n.Seq, n.Code, n.RetryAfterS)
	}
	return nil
}

// Validate implements Frame.
func (d Done) Validate() error {
	switch d.Reason {
	case DoneDrained, DoneBudget, DoneHeld, DoneError:
	default:
		return bad(TDone, "unknown reason %q", d.Reason)
	}
	if d.Backlog < 0 || d.Held < 0 {
		return bad(TDone, "negative counts")
	}
	return nil
}

// Validate implements Frame.
func (Bye) Validate() error { return nil }

// Writer writes frames, one Write call on the underlying writer per frame. It is not safe
// for concurrent use.
type Writer struct {
	w   io.Writer
	buf bytes.Buffer
}

// NewWriter returns a Writer on w.
func NewWriter(w io.Writer) *Writer { return &Writer{w: w} }

// Write validates and encodes f as one line. A Batch's records are copied byte for byte
// (they are not parsed, compacted or escaped); a record with a newline is an error.
func (w *Writer) Write(f Frame) error {
	if err := f.Validate(); err != nil {
		return err
	}
	w.buf.Reset()
	switch v := f.(type) {
	case Batch:
		w.encodeBatch(v)
	case *Batch:
		w.encodeBatch(*v)
	default:
		if a, ok := f.(*Ack); ok {
			f = *a
		}
		if a, ok := f.(Ack); ok { // lists are always arrays, never null
			if a.Accepted == nil {
				a.Accepted = []string{}
			}
			if a.Duplicates == nil {
				a.Duplicates = []string{}
			}
			if a.Rejected == nil {
				a.Rejected = []Rejected{}
			}
			f = a
		}
		var body bytes.Buffer
		enc := json.NewEncoder(&body)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(f); err != nil {
			return err
		}
		b := bytes.TrimRight(body.Bytes(), "\n")
		w.buf.WriteString(`{"t":"` + f.Type() + `"`)
		if len(b) > 2 { // not "{}"
			w.buf.WriteByte(',')
			w.buf.Write(b[1:])
		} else {
			w.buf.WriteByte('}')
		}
	}
	w.buf.WriteByte('\n')
	if w.buf.Len() > MaxLine {
		return ErrFrameTooLong
	}
	_, err := w.w.Write(w.buf.Bytes())
	return err
}

func (w *Writer) encodeBatch(b Batch) {
	w.buf.WriteString(`{"t":"batch","seq":`)
	w.buf.WriteString(strconv.Itoa(b.Seq))
	w.buf.WriteString(`,"records":[`)
	for i, r := range b.Records {
		if i > 0 {
			w.buf.WriteByte(',')
		}
		w.buf.Write(r)
	}
	w.buf.WriteString(`],"more":`)
	w.buf.WriteString(strconv.FormatBool(b.More))
	w.buf.WriteByte('}')
}

// Reader reads frames. It is not safe for concurrent use.
type Reader struct {
	r *bufio.Reader
}

// NewReader returns a Reader on r.
func NewReader(r io.Reader) *Reader { return &Reader{r: bufio.NewReaderSize(r, 64<<10)} }

// Read returns the next frame as a pointer to its struct (*Hello, *Batch, …). io.EOF means
// the stream ended between frames; a stream that ends inside a line is io.ErrUnexpectedEOF;
// a line over MaxLine, malformed JSON, a missing or unknown `t` or a frame that fails
// Validate is an error wrapping ErrProtocol. Reader never buffers more than MaxLine+1 bytes
// of a line.
func (r *Reader) Read() (Frame, error) {
	line, err := r.readLine()
	if err != nil {
		return nil, err
	}
	var head struct {
		T string `json:"t"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProtocol, err)
	}
	var f Frame
	switch head.T {
	case TBusy:
		f = &Busy{}
	case THello:
		f = &Hello{}
	case TStart:
		f = &Start{}
	case TRefuse:
		f = &Refuse{}
	case TBatch:
		f = &Batch{}
	case TAck:
		f = &Ack{}
	case TNack:
		f = &Nack{}
	case TDone:
		f = &Done{}
	case TBye:
		f = &Bye{}
	case "":
		return nil, fmt.Errorf("%w: frame has no t", ErrProtocol)
	default:
		return nil, fmt.Errorf("%w %q", ErrUnknownFrame, head.T)
	}
	if err := json.Unmarshal(line, f); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrProtocol, head.T, err)
	}
	if err := f.Validate(); err != nil {
		return nil, err
	}
	return f, nil
}

// readLine returns the next line without its newline, in a fresh slice (the caller's
// RawMessages alias it, so it must not be reused).
func (r *Reader) readLine() ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.r.ReadSlice('\n')
		if len(line)+len(chunk) > MaxLine {
			return nil, ErrFrameTooLong
		}
		line = append(line, chunk...)
		switch err {
		case nil:
			line = line[:len(line)-1]
			if len(line) == 0 {
				return nil, fmt.Errorf("%w: empty line", ErrProtocol)
			}
			return line, nil
		case bufio.ErrBufferFull:
			continue
		case io.EOF:
			if len(line) == 0 {
				return nil, io.EOF
			}
			return nil, io.ErrUnexpectedEOF
		default:
			return nil, err
		}
	}
}
