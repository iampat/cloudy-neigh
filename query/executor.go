package query

import (
	"time"

	cloudyneighpb "github.com/iampat/cloudy-neigh/proto/cloudyneigh/v1"
)

type SearchStats struct {
	ScanDuration        time.Duration
	MaterializeDuration time.Duration
}

type QueryExecutor interface {
	Search(
		col string,
		query []float32,
		topK int,
		metric cloudyneighpb.DistanceMetric,
		filter *cloudyneighpb.EqualityFilter,
	) ([]*cloudyneighpb.ScoredRecord, SearchStats, error)
}
