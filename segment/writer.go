package segment

import (
	"errors"
	"fmt"
	"io"

	"github.com/iampat/cloudy-neigh/namespace"
	storagepb "github.com/iampat/cloudy-neigh/proto/storage/v1"
	"github.com/iampat/cloudy-neigh/recordio"
	"google.golang.org/protobuf/proto"
)

const KeyPattern = "segments/%s/%s.recordio"

func Key(branch, segID string) string {
	return fmt.Sprintf(KeyPattern, branch, segID)
}

func RefKey(branch string, ref *storagepb.SegmentRef) string {
	if ref == nil {
		return ""
	}
	if ref.Key != "" {
		return ref.Key
	}
	return Key(branch, ref.SegmentId)
}

var ErrNilMutation = errors.New("segment: nil mutation")

type Writer struct {
	w   *recordio.Writer
	buf []byte
}

func NewWriter(w io.Writer) *Writer {
	return &Writer{w: recordio.NewWriter(w)}
}

func (w *Writer) Write(m *storagepb.DocumentMutation) error {
	if m == nil {
		return ErrNilMutation
	}
	if err := namespace.ValidateBranch(m.Branch); err != nil {
		return err
	}
	var err error
	w.buf, err = proto.MarshalOptions{}.MarshalAppend(w.buf[:0], m)
	if err != nil {
		return fmt.Errorf("segment write: marshal: %w", err)
	}
	if _, _, err := w.w.WriteRecord(w.buf); err != nil {
		return fmt.Errorf("segment write: %w", err)
	}
	return nil
}

func (w *Writer) Flush() error {
	return w.w.Flush()
}

func (w *Writer) Close() error {
	return w.w.Close()
}
