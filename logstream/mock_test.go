package logstream_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/iampat/cloudy-neigh/logstream"
	"github.com/iampat/cloudy-neigh/objectstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockStore struct {
	putFunc    func(ctx context.Context, key string, r io.Reader, cond objectstore.Condition) (string, error)
	getFunc    func(ctx context.Context, key string) (io.ReadCloser, objectstore.Object, error)
	existsFunc func(ctx context.Context, key string) (bool, error)
	listFunc   func(ctx context.Context, prefix, startAfter string, limit int) ([]objectstore.Object, error)
}

func (m *mockStore) Put(ctx context.Context, key string, r io.Reader, cond objectstore.Condition) (string, error) {
	if m.putFunc != nil {
		return m.putFunc(ctx, key, r, cond)
	}
	return "gen-1", nil
}

func (m *mockStore) Get(ctx context.Context, key string) (io.ReadCloser, objectstore.Object, error) {
	if m.getFunc != nil {
		return m.getFunc(ctx, key)
	}
	return nil, objectstore.Object{}, objectstore.ErrNotFound
}

func (m *mockStore) Exists(ctx context.Context, key string) (bool, error) {
	if m.existsFunc != nil {
		return m.existsFunc(ctx, key)
	}
	return false, nil
}

func (m *mockStore) Close() error {
	return nil
}

func (m *mockStore) Stat(ctx context.Context, key string) (objectstore.Object, error) {
	return objectstore.Object{}, nil
}

func (m *mockStore) Delete(ctx context.Context, key string) error {
	return nil
}

func (m *mockStore) List(ctx context.Context, prefix, startAfter string, limit int) ([]objectstore.Object, error) {
	if m.listFunc != nil {
		return m.listFunc(ctx, prefix, startAfter, limit)
	}
	return nil, nil
}

func TestNewWithMockStore(t *testing.T) {
	mock := &mockStore{}
	log, err := logstream.New(mock, "stream", nil)
	require.NoError(t, err)
	require.NotNil(t, log)
}

func TestAppendPutFailure(t *testing.T) {
	t.Run("PreconditionFailedCausesRetry", func(t *testing.T) {
		var putCalls int
		mock := &mockStore{
			putFunc: func(ctx context.Context, key string, r io.Reader, cond objectstore.Condition) (string, error) {
				putCalls++
				if putCalls == 1 {
					return "", objectstore.ErrPreconditionFailed
				}
				return "gen-1", nil
			},
			listFunc: func(ctx context.Context, prefix, startAfter string, limit int) ([]objectstore.Object, error) {
				return nil, nil
			},
		}

		log, err := logstream.New(mock, "stream", nil)
		require.NoError(t, err)

		seq, err := log.Append(context.Background(), []logstream.Record{[]byte("entry")})
		require.NoError(t, err)
		assert.Equal(t, uint64(2), seq)
		assert.Equal(t, 2, putCalls)
	})

	t.Run("OtherErrorFailsAppend", func(t *testing.T) {
		injectedErr := errors.New("write fault")
		mock := &mockStore{
			putFunc: func(ctx context.Context, key string, r io.Reader, cond objectstore.Condition) (string, error) {
				return "", injectedErr
			},
		}

		log, err := logstream.New(mock, "stream", nil)
		require.NoError(t, err)

		_, err = log.Append(context.Background(), []logstream.Record{[]byte("entry")})
		assert.ErrorIs(t, err, injectedErr)
	})
}

func TestReadGetFailure(t *testing.T) {
	t.Run("NotFoundBecomesEndOfStream", func(t *testing.T) {
		mock := &mockStore{
			getFunc: func(ctx context.Context, key string) (io.ReadCloser, objectstore.Object, error) {
				return nil, objectstore.Object{}, objectstore.ErrNotFound
			},
		}

		log, err := logstream.New(mock, "stream", nil)
		require.NoError(t, err)

		_, err = log.Read(context.Background(), 1)
		assert.ErrorIs(t, err, logstream.ErrEndOfStream)
	})

	t.Run("GeneralError", func(t *testing.T) {
		injectedErr := errors.New("read fault")
		mock := &mockStore{
			getFunc: func(ctx context.Context, key string) (io.ReadCloser, objectstore.Object, error) {
				return nil, objectstore.Object{}, injectedErr
			},
		}

		log, err := logstream.New(mock, "stream", nil)
		require.NoError(t, err)

		_, err = log.Read(context.Background(), 1)
		assert.ErrorIs(t, err, injectedErr)
	})
}

func TestTailAndHeadJump(t *testing.T) {
	t.Run("Empty", func(t *testing.T) {
		mock := &mockStore{
			listFunc: func(ctx context.Context, prefix, startAfter string, limit int) ([]objectstore.Object, error) {
				return nil, nil
			},
		}
		log, err := logstream.New(mock, "wal/stream", nil)
		require.NoError(t, err)

		tail, err := log.Tail(context.Background())
		require.NoError(t, err)
		assert.Equal(t, uint64(0), tail)
	})

	t.Run("ListUnderLimit", func(t *testing.T) {
		mock := &mockStore{
			listFunc: func(ctx context.Context, prefix, startAfter string, limit int) ([]objectstore.Object, error) {
				return []objectstore.Object{
					{Key: fmt.Sprintf("%s%020d.recordio", prefix, 1)},
					{Key: fmt.Sprintf("%s%020d.recordio", prefix, 2)},
					{Key: fmt.Sprintf("%s%020d.recordio", prefix, 3)},
				}, nil
			},
		}
		log, err := logstream.New(mock, "wal/stream", nil)
		require.NoError(t, err)

		tail, err := log.Tail(context.Background())
		require.NoError(t, err)
		assert.Equal(t, uint64(3), tail)
	})

	t.Run("JumpProbeWhenPageFull", func(t *testing.T) {
		const targetHead = uint64(1050)
		var probeCount int
		mock := &mockStore{
			listFunc: func(ctx context.Context, prefix, startAfter string, limit int) ([]objectstore.Object, error) {
				objs := make([]objectstore.Object, 1000)
				for i := 0; i < 1000; i++ {
					objs[i] = objectstore.Object{
						Key: fmt.Sprintf("%s%020d.recordio", prefix, i+1),
					}
				}
				return objs, nil
			},
			existsFunc: func(ctx context.Context, key string) (bool, error) {
				probeCount++
				var seq uint64
				_, err := fmt.Sscanf(key, "wal/stream/%020d.recordio", &seq)
				if err != nil {
					return false, err
				}
				return seq <= targetHead, nil
			},
		}
		log, err := logstream.New(mock, "wal/stream", nil)
		require.NoError(t, err)

		tail, err := log.Tail(context.Background())
		require.NoError(t, err)
		assert.Equal(t, targetHead, tail)
		assert.Positive(t, probeCount)
	})
}
