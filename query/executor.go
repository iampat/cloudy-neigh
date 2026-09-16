package query

import (
	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
)

type QueryExecutor interface {
	Upsert(id string, vectors map[string][]float32, attrs map[string]*cloudyneighpb.AttributeValue) error
	UpsertRecord(rec *cloudyneighpb.Record) error
	Delete(id string) bool
	Get(id string) (*cloudyneighpb.Record, bool)
	Vector(id, col string) ([]float32, bool)
	Attribute(id, key string) (*cloudyneighpb.AttributeValue, bool)
	Search(col string, query []float32, topK int, metric cloudyneighpb.DistanceMetric, filter *cloudyneighpb.EqualityFilter) ([]*cloudyneighpb.ScoredRecord, error)
	SearchWithStats(col string, query []float32, topK int, metric cloudyneighpb.DistanceMetric, filter *cloudyneighpb.EqualityFilter) ([]*cloudyneighpb.ScoredRecord, SearchStats, error)
}
