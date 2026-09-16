package query

import (
	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
)

type QueryExecutor interface {
	Search(
		col string,
		query []float32,
		topK int,
		metric cloudyneighpb.DistanceMetric,
		filter *cloudyneighpb.EqualityFilter,
	) ([]*cloudyneighpb.ScoredRecord, SearchStats, error)
}
