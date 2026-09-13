package grpcapi

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/iampat/cloudy-neigh/logstream"
	"github.com/iampat/cloudy-neigh/namespace"
	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
	storagepb "github.com/iampat/cloudy-neigh/proto/storage/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

var ErrNilLog = errors.New("grpcapi: nil log")

type IngestServer struct {
	cloudyneighpb.UnimplementedIngestServiceServer
	log *logstream.Log
}

func NewIngestServer(log *logstream.Log) (*IngestServer, error) {
	if log == nil {
		return nil, ErrNilLog
	}
	return &IngestServer{log: log}, nil
}

func (s *IngestServer) Upsert(ctx context.Context, req *cloudyneighpb.UpsertRequest) (*cloudyneighpb.UpsertResponse, error) {
	start := time.Now()
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "grpcapi: nil request")
	}
	if err := namespace.ValidateNamespace(req.Namespace); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "grpcapi: invalid namespace: %v", err)
	}
	if len(req.Records) == 0 {
		return &cloudyneighpb.UpsertResponse{}, nil
	}

	records := make([]logstream.Record, len(req.Records))
	for i, rec := range req.Records {
		if rec == nil {
			return nil, status.Errorf(codes.InvalidArgument, "grpcapi: record at index %d is nil", i)
		}
		if rec.Id == "" {
			return nil, status.Errorf(codes.InvalidArgument, "grpcapi: record at index %d has empty id", i)
		}
		payload, err := proto.Marshal(rec)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "grpcapi: marshal record: %v", err)
		}
		walRec := &storagepb.WalRecord{
			Record: &storagepb.WalRecord_Mutation{
				Mutation: &storagepb.DocumentMutation{
					Branch:  req.Namespace,
					DocId:   rec.Id,
					Op:      storagepb.MutationOp_PUT,
					Payload: payload,
				},
			},
		}
		recBytes, err := proto.Marshal(walRec)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "grpcapi: marshal wal record: %v", err)
		}
		records[i] = recBytes
	}

	appendStart := time.Now()
	if _, err := s.log.Append(ctx, records); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, status.Error(codes.Canceled, err.Error())
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, status.Error(codes.DeadlineExceeded, err.Error())
		}
		return nil, status.Errorf(codes.Internal, "grpcapi: append wal: %v", err)
	}
	appendDur := time.Since(appendStart)

	slog.Debug("upsert",
		"namespace", req.Namespace,
		"records", len(req.Records),
		"append_dur", appendDur,
		"total_dur", time.Since(start),
	)

	return &cloudyneighpb.UpsertResponse{
		UpsertedCount: uint32(len(req.Records)),
	}, nil
}

func (s *IngestServer) Delete(ctx context.Context, req *cloudyneighpb.DeleteRequest) (*cloudyneighpb.DeleteResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "grpcapi: nil request")
	}
	if err := namespace.ValidateNamespace(req.Namespace); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "grpcapi: invalid namespace: %v", err)
	}
	if len(req.Ids) == 0 {
		return &cloudyneighpb.DeleteResponse{}, nil
	}

	records := make([]logstream.Record, len(req.Ids))
	for i, id := range req.Ids {
		if id == "" {
			return nil, status.Errorf(codes.InvalidArgument, "grpcapi: id at index %d is empty", i)
		}
		rec := &storagepb.WalRecord{
			Record: &storagepb.WalRecord_Mutation{
				Mutation: &storagepb.DocumentMutation{
					Branch: req.Namespace,
					DocId:  id,
					Op:     storagepb.MutationOp_DELETE,
				},
			},
		}
		recBytes, err := proto.Marshal(rec)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "grpcapi: marshal wal record: %v", err)
		}
		records[i] = recBytes
	}

	if _, err := s.log.Append(ctx, records); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, status.Error(codes.Canceled, err.Error())
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, status.Error(codes.DeadlineExceeded, err.Error())
		}
		return nil, status.Errorf(codes.Internal, "grpcapi: append wal: %v", err)
	}

	return &cloudyneighpb.DeleteResponse{
		DeletedCount: uint32(len(req.Ids)),
	}, nil
}
