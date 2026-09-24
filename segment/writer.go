package segment

import (
	"errors"
	"fmt"
	"io"

	storagepb "github.com/iampat/cloudy-neigh/proto/storage/v1"
	"github.com/iampat/cloudy-neigh/recordio"
	"google.golang.org/protobuf/proto"
)

const KeyPattern = "segments/%s.recordio"

func Key(segID string) string {
	return fmt.Sprintf(KeyPattern, segID)
}

var ErrNilMutation = errors.New("segment: nil mutation")

type Writer struct {
	*recordio.Writer
	buf []byte
}

func NewWriter(w io.Writer) *Writer {
	return &Writer{Writer: recordio.NewWriter(w)}
}

func (w *Writer) Write(m *storagepb.DocumentMutation) error {
	if m == nil {
		return ErrNilMutation
	}
	var err error
	w.buf, err = proto.MarshalOptions{}.MarshalAppend(w.buf[:0], m)
	if err != nil {
		return fmt.Errorf("segment write: marshal: %w", err)
	}
	if _, _, err := w.WriteRecord(w.buf); err != nil {
		return fmt.Errorf("segment write: %w", err)
	}
	return nil
}
