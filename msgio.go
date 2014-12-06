package msgio

import (
	"encoding/binary"
	"io"

	multipool "github.com/jbenet/go-msgio/multipool"
)

// NBO is NetworkByteOrder
var NBO = binary.BigEndian

const lengthSize = 4

// Writer is the msgio Writer interface. It writes len-framed messages.
type Writer interface {

	// Write writes passed in buffer as a single message.
	Write([]byte) error

	// WriteMsg writes the msg in the passed in buffer.
	WriteMsg([]byte) error
}

// WriteCloser is a Writer + Closer interface. Like in `golang/pkg/io`
type WriteCloser interface {
	Writer
	io.Closer
}

// Reader is the msgio Reader interface. It reads len-framed messages.
type Reader interface {

	// Read reads the next message from the Reader.
	// The client must pass a buffer large enough, or io.ErrShortBuffer will be
	// returned.
	Read([]byte) (int, error)

	// ReadMsg reads the next message from the Reader.
	// Uses a multipool.Pool internally to reuse buffers. io.ErrShortBuffer will
	// be returned if the Pool.Get(...) returns nil.
	// User may call ReleaseMsg(msg) to signal a buffer can be reused.
	ReadMsg() ([]byte, error)

	// ReleaseMsg signals a buffer can be reused.
	ReleaseMsg([]byte)
}

// ReadCloser combines a Reader and Closer.
type ReadCloser interface {
	Reader
	io.Closer
}

// ReadWriter combines a Reader and Writer.
type ReadWriter interface {
	Reader
	Writer
}

// ReadWriteCloser combines a Reader, a Writer, and Closer.
type ReadWriteCloser interface {
	Reader
	Writer
	io.Closer
}

// writer is the underlying type that implements the Writer interface.
type writer struct {
	W io.Writer
}

// NewWriter wraps an io.Writer with a msgio framed writer. The msgio.Writer
// will write the length prefix of every message written.
func NewWriter(w io.Writer) WriteCloser {
	return &writer{w}
}

func (s *writer) Write(msg []byte) (err error) {
	return s.WriteMsg(msg)
}

func (s *writer) WriteMsg(msg []byte) (err error) {
	length := uint32(len(msg))
	if err := binary.Write(s.W, NBO, &length); err != nil {
		return err
	}
	_, err = s.W.Write(msg)
	return err
}

func (s *writer) Close() error {
	if c, ok := s.W.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// reader is the underlying type that implements the Reader interface.
type reader struct {
	R io.Reader

	lbuf []byte
	next int
	pool *multipool.Pool
}

// NewReader wraps an io.Reader with a msgio framed reader. The msgio.Reader
// will read whole messages at a time (using the length). Assumes an equivalent
// writer on the other side.
func NewReader(r io.Reader) ReadCloser {
	return NewReaderWithPool(r, &multipool.ByteSlicePool)
}

// NewReaderWithPool wraps an io.Reader with a msgio framed reader. The msgio.Reader
// will read whole messages at a time (using the length). Assumes an equivalent
// writer on the other side.  It uses a given multipool.Pool
func NewReaderWithPool(r io.Reader, p *multipool.Pool) ReadCloser {
	if p == nil {
		panic("nil pool")
	}
	return &reader{r, make([]byte, lengthSize), -1, p}
}

// nextMsgLen reads the length of the next msg into s.lbuf, and returns it.
// WARNING: like ReadMsg, nextMsgLen is destructive. It reads from the internal
// reader.
func (s *reader) nextMsgLen() (int, error) {
	if s.next == -1 {
		if _, err := io.ReadFull(s.R, s.lbuf); err != nil {
			return 0, err
		}
		s.next = int(NBO.Uint32(s.lbuf))
	}
	return s.next, nil
}

func (s *reader) Read(msg []byte) (int, error) {
	length, err := s.nextMsgLen()
	if err != nil {
		return 0, err
	}

	if length > len(msg) {
		return 0, io.ErrShortBuffer
	}
	_, err = io.ReadFull(s.R, msg[:length])
	s.next = -1 // signal we've consumed this msg
	return length, err
}

func (s *reader) ReadMsg() ([]byte, error) {
	length, err := s.nextMsgLen()
	if err != nil {
		return nil, err
	}

	msgb := s.pool.Get(uint32(length))
	if msgb == nil {
		return nil, io.ErrShortBuffer
	}
	msg := msgb.([]byte)[:length]
	_, err = io.ReadFull(s.R, msg)
	s.next = -1 // signal we've consumed this msg
	return msg, err
}

func (s *reader) ReleaseMsg(msg []byte) {
	c := cap(msg)
	if c > multipool.MaxLength {
		c = multipool.MaxLength
	}
	s.pool.Put(uint32(c), msg)
}

func (s *reader) Close() error {
	if c, ok := s.R.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// readWriter is the underlying type that implements a ReadWriter.
type readWriter struct {
	Reader
	Writer
}

// NewReadWriter wraps an io.ReadWriter with a msgio.ReadWriter. Writing
// and Reading will be appropriately framed.
func NewReadWriter(rw io.ReadWriter) ReadWriter {
	return &readWriter{
		Reader: NewReader(rw),
		Writer: NewWriter(rw),
	}
}

func (rw *readWriter) Close() error {
	var errs []error

	if w, ok := rw.Writer.(WriteCloser); ok {
		if err := w.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if r, ok := rw.Reader.(ReadCloser); ok {
		if err := r.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return multiErr(errs)
	}
	return nil
}

// multiErr is a util to return multiple errors
type multiErr []error

func (m multiErr) Error() string {
	if len(m) == 0 {
		return "no errors"
	}

	s := "Multiple errors: "
	for i, e := range m {
		if i != 0 {
			s += ", "
		}
		s += e.Error()
	}
	return s
}
