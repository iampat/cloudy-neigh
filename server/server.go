// Package server serves the Index service defined in docs/design/grpc-api.md.
package server

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/iampat/cloudy-neigh/index"
	"github.com/iampat/cloudy-neigh/proto/cloudyneigh"
)

type Index struct {
	cloudyneigh.UnimplementedIndexAPIServer

	store *index.Store
}

func New(store *index.Store) *Index {
	return &Index{store: store}
}

func (s *Index) Write(ctx context.Context, req *cloudyneigh.WriteRequest) (*cloudyneigh.WriteResponse, error) {
	// The whole request is checked before any of it is stored. That is what
	// makes a batch apply to every document or to none.
	docs, err := documentsToUpsert(req)
	if err != nil {
		return nil, err
	}
	if err := s.store.Upsert(ctx, req.GetNamespace(), docs); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, status.FromContextError(ctxErr).Err()
		}
		return nil, status.Errorf(codes.Internal, "upsert: %v", err)
	}
	return &cloudyneigh.WriteResponse{}, nil
}

func (s *Index) Query(ctx context.Context, req *cloudyneigh.QueryRequest) (*cloudyneigh.QueryResponse, error) {
	id, err := lookupID(req)
	if err != nil {
		return nil, err
	}

	// A namespace appears on its first write, so an unknown namespace and an
	// unknown ID are the same state: no match, and no error.
	doc, err := s.store.Lookup(ctx, req.GetNamespace(), id)
	if errors.Is(err, index.ErrNotFound) {
		return &cloudyneigh.QueryResponse{}, nil
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, status.FromContextError(ctxErr).Err()
		}
		return nil, status.Errorf(codes.Internal, "lookup: %v", err)
	}
	return &cloudyneigh.QueryResponse{
		Matches: []*cloudyneigh.Match{{Document: doc}},
	}, nil
}
