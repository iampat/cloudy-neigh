package server

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/iampat/cloudy-neigh/proto/cloudyneigh"
)

const idAttribute = "id"

func documentsToUpsert(req *cloudyneigh.WriteRequest) ([]*cloudyneigh.Document, error) {
	if req.GetNamespace() == "" {
		return nil, status.Error(codes.InvalidArgument, "namespace is required")
	}
	upsert := req.GetUpsert()
	if upsert == nil {
		return nil, status.Error(codes.InvalidArgument, "operation is required")
	}

	docs := upsert.GetDocuments()
	seen := make(map[string]struct{}, len(docs))
	for i, doc := range docs {
		id := doc.GetId()
		if id == "" {
			return nil, status.Errorf(codes.InvalidArgument, "documents[%d]: id is required", i)
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, status.Errorf(codes.InvalidArgument, "documents[%d]: duplicate id %q", i, id)
		}
		seen[id] = struct{}{}

		for name, value := range doc.GetAttributes() {
			if name == idAttribute {
				return nil, status.Errorf(codes.InvalidArgument,
					"documents[%d]: %q is a reserved attribute name", i, idAttribute)
			}
			if value.GetKind() == nil {
				return nil, status.Errorf(codes.InvalidArgument,
					"documents[%d]: attribute %q has no value", i, name)
			}
		}
	}
	return docs, nil
}

func lookupID(req *cloudyneigh.QueryRequest) (string, error) {
	if req.GetNamespace() == "" {
		return "", status.Error(codes.InvalidArgument, "namespace is required")
	}
	node := req.GetQuery()
	if node == nil {
		return "", status.Error(codes.InvalidArgument, "query is required")
	}
	retrieve := node.GetRetrieve()
	if retrieve == nil {
		return "", status.Error(codes.InvalidArgument, "query.retrieve is required")
	}
	filter := retrieve.GetFilter()
	if filter == nil {
		return "", status.Error(codes.InvalidArgument, "query.retrieve.filter is required")
	}
	compare := filter.GetCompare()
	if compare == nil {
		return "", status.Error(codes.InvalidArgument, "query.retrieve.filter.compare is required")
	}
	if compare.GetAttribute() != idAttribute {
		// Unimplemented, not InvalidArgument: the contract allows this filter
		// and a later build may serve it, so the same request is worth a retry.
		return "", status.Errorf(codes.Unimplemented,
			"filter on attribute %q is not supported, only %q", compare.GetAttribute(), idAttribute)
	}

	eq := compare.GetEq()
	if eq == nil {
		return "", status.Error(codes.InvalidArgument, "compare.eq is required")
	}
	text, ok := eq.GetKind().(*cloudyneigh.Value_Text)
	if !ok {
		return "", status.Error(codes.InvalidArgument, "compare.eq must hold a text value")
	}
	if text.Text == "" {
		return "", status.Error(codes.InvalidArgument, "compare.eq text must not be empty")
	}
	return text.Text, nil
}
