package main

import (
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/iampat/cloudy-neigh/proto/cloudyneigh"
)

func dial(addr string) (*grpc.ClientConn, cloudyneigh.IndexAPIClient, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, fmt.Errorf("connect to %s: %w", addr, err)
	}
	return conn, cloudyneigh.NewIndexAPIClient(conn), nil
}

func textValue(s string) *cloudyneigh.Value {
	return &cloudyneigh.Value{Kind: &cloudyneigh.Value_Text{Text: s}}
}

func queryByID(namespace, id string) *cloudyneigh.QueryRequest {
	return &cloudyneigh.QueryRequest{
		Namespace: namespace,
		Query: &cloudyneigh.QueryNode{
			Kind: &cloudyneigh.QueryNode_Retrieve{
				Retrieve: &cloudyneigh.Retrieve{
					Filter: &cloudyneigh.Filter{
						Kind: &cloudyneigh.Filter_Compare{
							Compare: &cloudyneigh.Compare{
								Attribute: "id",
								Predicate: &cloudyneigh.Compare_Eq{Eq: textValue(id)},
							},
						},
					},
				},
			},
		},
	}
}
