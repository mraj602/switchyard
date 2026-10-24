package drainproto

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func roundTrip(t *testing.T, f Frame) Frame {
	t.Helper()
	var buf bytes.Buffer
	if err := NewWriter(&buf).Write(f); err != nil {
		t.Fatalf("write %T: %v", f, err)
	}
	if !strings.HasSuffix(buf.String(), "\n") || strings.Count(buf.String(), "\n") != 1 {
		t.Fatalf("%T is not exactly one line: %q", f, buf.String())
	}
	if !strings.HasPrefix(buf.String(), `{"t":"`+f.Type()+`"`) {
		t.Fatalf("%T: t not first: %q", f, buf.String())
	}
	got, err := NewReader(&buf).Read()
	if err != nil {
		t.Fatalf("read %T: %v", f, err)
	}
	return got
}

func TestFramesRoundTrip(t *testing.T) {
	oldest := "2026-10-11T12:02:00.000Z"
	cases := []Frame{
		Busy{},
		Hello{Proto: Proto, Machine: "m_01JA2W5N0G8H3Q7S2V6X9Y1Z4K", Name: "gpu-box", Wy: "0.5.0", OS: "linux", Arch: "arm64", RecordV: 1, Backlog: 1240, Oldest: &oldest},
		Hello{Proto: Proto, Machine: "m_x", Backlog: 0}, // oldest null
		Start{Proto: Proto, Hub: "0.5.0", RecordVMax: 1, MaxRecords: 500, MaxBytes: 4 << 20, BudgetS: 30},
		Refuse{Code: RefuseFrozen, Message: "hub is frozen for a move"},
		Batch{Seq: 1, Records: []json.RawMessage{json.RawMessage(`{"id":"a"}`), json.RawMessage(`{"id":"b"}`)}, More: true},
		Ack{Seq: 1, Accepted: []string{"a"}, Duplicates: []string{}, Rejected: []Rejected{{ID: "b", Reason: "bad_payload", Detail: "<x>"}}},
		Nack{Seq: 2, Code: NackUnavailable, RetryAfterS: 30},
		Done{Reason: DoneHeld, Backlog: 3, Held: 3},
		Bye{},
	}
	for _, f := range cases {
		got := roundTrip(t, f)
		want := f
		if reflect.TypeOf(want).Kind() != reflect.Ptr {
			p := reflect.New(reflect.TypeOf(want))
			p.Elem().Set(reflect.ValueOf(want))
			want = p.Interface().(Frame)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%T: got %+v want %+v", f, got, want)
		}
	}
}

func TestHelloOldestNullAndAckListsNeverNull(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	if err := w.Write(Hello{Proto: Proto, Machine: "m_x"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"oldest":null`) {
		t.Fatalf("oldest must be null: %s", buf.String())
	}
	buf.Reset()
	if err := w.Write(&Ack{Seq: 1}); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{`"accepted":[]`, `"duplicates":[]`, `"rejected":[]`} {
		if !strings.Contains(buf.String(), k) {
			t.Fatalf("%s missing in %s", k, buf.String())
		}
	}
}

// A record keeps its exact bytes: odd spacing, key order, escapes and numbers that a
// decode/encode would change.
func TestBatchRecordsSurviveByteForByte(t *testing.T) {
	odd := []string{
		`{ "z" : 1,"a":  [1 , 2.0,  1e3] ,"id" :"x"}`,
		`{"id":"<&>é","n":12345678901234567890,"f":1.0}`,
		"{\t\"k\":\t\"v\"\t}",
		`{"nested":{"b":1,"a":2},"id":"\/slash"}`,
	}
	var recs []json.RawMessage
	for _, o := range odd {
		recs = append(recs, json.RawMessage(o))
	}
	var buf bytes.Buffer
	if err := NewWriter(&buf).Write(Batch{Seq: 7, Records: recs, More: false}); err != nil {
		t.Fatal(err)
	}
	for _, o := range odd {
		if !strings.Contains(buf.String(), o) {
			t.Fatalf("writer changed %q in %s", o, buf.String())
		}
	}
	f, err := NewReader(&buf).Read()
	if err != nil {
		t.Fatal(err)
	}
	b := f.(*Batch)
	if b.Seq != 7 || b.More || len(b.Records) != len(odd) {
		t.Fatalf("batch: %+v", b)
	}
	for i, o := range odd {
		if string(b.Records[i]) != o {
			t.Errorf("record %d: got %q want %q", i, b.Records[i], o)
		}
	}
}

func TestWriterRejectsBadBatches(t *testing.T) {
	w := NewWriter(io.Discard)
	for name, b := range map[string]Batch{
		"newline":    {Seq: 1, Records: []json.RawMessage{json.RawMessage("{\"a\":\n1}")}},
		"not object": {Seq: 1, Records: []json.RawMessage{json.RawMessage(`[1]`)}},
		"empty":      {Seq: 1},
		"seq":        {Seq: 0, Records: []json.RawMessage{json.RawMessage(`{}`)}},
		"too many":   {Seq: 1, Records: make([]json.RawMessage, MaxRecords+1)},
	} {
		if err := w.Write(b); !errors.Is(err, ErrProtocol) {
			t.Errorf("%s: %v", name, err)
		}
	}
	big := json.RawMessage(`{"p":"` + strings.Repeat("x", 3<<20) + `"}`)
	if err := w.Write(Batch{Seq: 1, Records: []json.RawMessage{big, big}}); !errors.Is(err, ErrProtocol) {
		t.Errorf("over MaxBytes: %v", err)
	}
}

func TestReaderErrors(t *testing.T) {
	read := func(s string) error {
		_, err := NewReader(strings.NewReader(s)).Read()
		return err
	}
	if err := read(""); err != io.EOF {
		t.Errorf("empty stream: %v", err)
	}
	if err := read(`{"t":"bye"}`); err != io.ErrUnexpectedEOF {
		t.Errorf("no newline: %v", err)
	}
	for name, in := range map[string]string{
		"unknown t":      `{"t":"explode"}` + "\n",
		"no t":           `{"seq":1}` + "\n",
		"not json":       "hello\n",
		"empty line":     "\n",
		"bad refuse":     `{"t":"refuse","code":"because"}` + "\n",
		"start no limit": `{"t":"start","proto":1,"record_v_max":1}` + "\n",
		"ack no seq":     `{"t":"ack","accepted":[]}` + "\n",
		"done reason":    `{"t":"done","reason":"meh"}` + "\n",
		"batch string":   `{"t":"batch","seq":1,"records":["x"]}` + "\n",
	} {
		if err := read(in); !errors.Is(err, ErrProtocol) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := read(`{"t":"explode"}` + "\n"); !errors.Is(err, ErrUnknownFrame) {
		t.Errorf("unknown t not ErrUnknownFrame: %v", err)
	}
}

func TestReaderIgnoresUnknownFieldsAndReadsSeveralFrames(t *testing.T) {
	in := `{"t":"nack","seq":3,"code":"unavailable","retry_after_s":5,"future":{"a":1}}` + "\n" +
		`{"t":"bye","why":"budget"}` + "\n"
	r := NewReader(strings.NewReader(in))
	f, err := r.Read()
	if err != nil {
		t.Fatal(err)
	}
	if n, ok := f.(*Nack); !ok || n.Seq != 3 || n.RetryAfterS != 5 {
		t.Fatalf("%#v", f)
	}
	if f, err = r.Read(); err != nil {
		t.Fatal(err)
	} else if _, ok := f.(*Bye); !ok {
		t.Fatalf("%#v", f)
	}
	if _, err = r.Read(); err != io.EOF {
		t.Fatalf("want EOF, got %v", err)
	}
}

func TestLineLimit(t *testing.T) {
	// The longest legal line passes; one byte more does not (newline included).
	prefix := `{"t":"refuse","code":"proto","message":"`
	suffix := `"}`
	fill := MaxLine - 1 - len(prefix) - len(suffix) // leaves room for "\n"
	ok := prefix + strings.Repeat("x", fill) + suffix + "\n"
	if len(ok) != MaxLine {
		t.Fatalf("test line is %d bytes", len(ok))
	}
	if _, err := NewReader(strings.NewReader(ok)).Read(); err != nil {
		t.Fatalf("line of exactly MaxLine: %v", err)
	}
	long := prefix + strings.Repeat("x", fill+1) + suffix + "\n"
	if _, err := NewReader(strings.NewReader(long)).Read(); !errors.Is(err, ErrFrameTooLong) {
		t.Fatalf("over MaxLine: %v", err)
	}
	if err := NewWriter(io.Discard).Write(Refuse{Code: RefuseProto, Message: strings.Repeat("x", MaxLine)}); !errors.Is(err, ErrFrameTooLong) {
		t.Fatalf("writer over MaxLine: %v", err)
	}
}

func TestAckIDs(t *testing.T) {
	a := Ack{Seq: 1, Accepted: []string{"a"}, Duplicates: []string{"b"}, Rejected: []Rejected{{ID: "c"}}}
	if got := strings.Join(a.IDs(), ","); got != "a,b,c" {
		t.Fatal(got)
	}
}
