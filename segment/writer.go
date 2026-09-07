package segment

import (
	"errors"
	"fmt"
	"io"

	storagepb "github.com/iampat/cloudy-neigh/proto/storage/v1"
	"github.com/iampat/cloudy-neigh/recordio"
	"google.golang.org/protobuf/proto"
)

var (
	ErrInvalidBranchName = errors.New("segment: invalid branch name")
	ErrNilMutation       = errors.New("segment: nil mutation")
)

func validateBranch(branch string) error {
	if branch == "" {
		return fmt.Errorf("%w: empty branch name", ErrInvalidBranchName)
	}

	first := branch[0]
	if !((first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z')) {
		return fmt.Errorf("%w: must start with letter: %q", ErrInvalidBranchName, branch)
	}

	for i := 1; i < len(branch); i++ {
		c := branch[i]
		switch {
		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
		case c >= '0' && c <= '9':
		case c == '-' || c == '_':
		default:
			return fmt.Errorf("%w: invalid character %q in %q", ErrInvalidBranchName, c, branch)
		}
	}
	return nil
}

type Writer struct {
	w *recordio.Writer
}

func NewWriter(w io.Writer) *Writer {
	return &Writer{w: recordio.NewWriter(w)}
}

func (w *Writer) Write(m *storagepb.DocumentMutation) error {
	if m == nil {
		return ErrNilMutation
	}
	if err := validateBranch(m.Branch); err != nil {
		return err
	}
	data, err := proto.Marshal(m)
	if err != nil {
		return fmt.Errorf("segment write: marshal: %w", err)
	}
	if _, _, err := w.w.WriteRecord(data); err != nil {
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
