package segment

import (
	"fmt"
	"io"

	storagepb "github.com/iampat/cloudy-neigh/proto/storage/v1"
	"github.com/iampat/cloudy-neigh/recordio"
	"google.golang.org/protobuf/proto"
)

type Reader struct {
	s *recordio.Scanner
}

func NewReader(r io.Reader) *Reader {
	return &Reader{s: recordio.NewScanner(r)}
}

func (r *Reader) Next() (*storagepb.DocumentMutation, error) {
	if !r.s.Scan() {
		if err := r.s.Err(); err != nil {
			return nil, err
		}
		return nil, io.EOF
	}

	var m storagepb.DocumentMutation
	if err := proto.Unmarshal(r.s.Record(), &m); err != nil {
		return nil, fmt.Errorf("segment read: unmarshal: %w", err)
	}
	return &m, nil
}
