package server_test

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	"github.com/iampat/cloudy-neigh/cas"
	"github.com/iampat/cloudy-neigh/index"
	"github.com/iampat/cloudy-neigh/proto/cloudyneigh"
	"github.com/iampat/cloudy-neigh/server"
)

func newClient(t *testing.T) cloudyneigh.IndexAPIClient {
	t.Helper()

	store, err := index.Open(cas.NewMemory())
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	listener := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	cloudyneigh.RegisterIndexAPIServer(srv, server.New(store))

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(listener) }()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	t.Cleanup(func() {
		conn.Close()
		srv.GracefulStop()
		if err := <-serveErr; err != nil {
			t.Errorf("serve: %v", err)
		}
	})
	return cloudyneigh.NewIndexAPIClient(conn)
}

func textDocument(id, content string) *cloudyneigh.Document {
	return &cloudyneigh.Document{
		Id: id,
		Attributes: map[string]*cloudyneigh.Value{
			"content": {Kind: &cloudyneigh.Value_Text{Text: content}},
		},
	}
}

func writeRequest(namespace string, docs ...*cloudyneigh.Document) *cloudyneigh.WriteRequest {
	return &cloudyneigh.WriteRequest{
		Namespace: namespace,
		Operation: &cloudyneigh.WriteRequest_Upsert{
			Upsert: &cloudyneigh.Upsert{Documents: docs},
		},
	}
}

func queryRequest(namespace string, node *cloudyneigh.QueryNode) *cloudyneigh.QueryRequest {
	return &cloudyneigh.QueryRequest{Namespace: namespace, Query: node}
}

func retrieveNode(filter *cloudyneigh.Filter) *cloudyneigh.QueryNode {
	return &cloudyneigh.QueryNode{
		Kind: &cloudyneigh.QueryNode_Retrieve{
			Retrieve: &cloudyneigh.Retrieve{Filter: filter},
		},
	}
}

func compareFilter(c *cloudyneigh.Compare) *cloudyneigh.Filter {
	return &cloudyneigh.Filter{Kind: &cloudyneigh.Filter_Compare{Compare: c}}
}

func idEquals(id string) *cloudyneigh.Compare {
	return &cloudyneigh.Compare{
		Attribute: "id",
		Predicate: &cloudyneigh.Compare_Eq{
			Eq: &cloudyneigh.Value{Kind: &cloudyneigh.Value_Text{Text: id}},
		},
	}
}

func TestWriteThenQuery(t *testing.T) {
	client := newClient(t)
	ctx := t.Context()

	want := textDocument("src/index/writer.go", "package index")
	if _, err := client.Write(ctx, writeRequest("repo", want)); err != nil {
		t.Fatalf("Write: %v", err)
	}

	resp, err := client.Query(ctx, queryRequest("repo", retrieveNode(compareFilter(idEquals(want.GetId())))))
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(resp.GetMatches()) != 1 {
		t.Fatalf("Query returned %d matches, want 1", len(resp.GetMatches()))
	}
	if got := resp.GetMatches()[0].GetDocument(); !proto.Equal(got, want) {
		t.Errorf("Query returned %v, want %v", got, want)
	}
}

func TestQueryOfAnUnwrittenIDIsNotAnError(t *testing.T) {
	client := newClient(t)

	resp, err := client.Query(t.Context(),
		queryRequest("repo", retrieveNode(compareFilter(idEquals("never written")))))
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(resp.GetMatches()) != 0 {
		t.Errorf("Query returned %d matches, want 0", len(resp.GetMatches()))
	}
}

func TestRejectedWriteStoresNothing(t *testing.T) {
	client := newClient(t)
	ctx := t.Context()

	good := textDocument("good", "content")
	_, err := client.Write(ctx, writeRequest("repo", good, textDocument("", "no id")))
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("Write error = %v, want InvalidArgument", err)
	}

	resp, err := client.Query(ctx, queryRequest("repo", retrieveNode(compareFilter(idEquals("good")))))
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(resp.GetMatches()) != 0 {
		t.Errorf("the valid document of a rejected batch was stored")
	}
}

func TestWriteRejections(t *testing.T) {
	tests := []struct {
		name string
		req  *cloudyneigh.WriteRequest
		want codes.Code
	}{
		{"no namespace", writeRequest("", textDocument("a", "x")), codes.InvalidArgument},
		{"no operation", &cloudyneigh.WriteRequest{Namespace: "repo"}, codes.InvalidArgument},
		{"no id", writeRequest("repo", textDocument("", "x")), codes.InvalidArgument},
		{
			"duplicate id",
			writeRequest("repo", textDocument("a", "x"), textDocument("a", "y")),
			codes.InvalidArgument,
		},
		{
			"reserved attribute name",
			writeRequest("repo", &cloudyneigh.Document{
				Id: "a",
				Attributes: map[string]*cloudyneigh.Value{
					"id": {Kind: &cloudyneigh.Value_Text{Text: "a"}},
				},
			}),
			codes.InvalidArgument,
		},
		{
			"attribute without a value",
			writeRequest("repo", &cloudyneigh.Document{
				Id:         "a",
				Attributes: map[string]*cloudyneigh.Value{"content": {}},
			}),
			codes.InvalidArgument,
		},
	}

	client := newClient(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := client.Write(t.Context(), tt.req)
			if got := status.Code(err); got != tt.want {
				t.Errorf("Write code = %s, want %s (error %v)", got, tt.want, err)
			}
		})
	}
}

func TestQueryRejections(t *testing.T) {
	tests := []struct {
		name string
		req  *cloudyneigh.QueryRequest
		want codes.Code
	}{
		{
			"no namespace",
			queryRequest("", retrieveNode(compareFilter(idEquals("a")))),
			codes.InvalidArgument,
		},
		{"no query", &cloudyneigh.QueryRequest{Namespace: "repo"}, codes.InvalidArgument},
		{"no retrieve", queryRequest("repo", &cloudyneigh.QueryNode{}), codes.InvalidArgument},
		{"no filter", queryRequest("repo", retrieveNode(nil)), codes.InvalidArgument},
		{
			"no compare",
			queryRequest("repo", retrieveNode(&cloudyneigh.Filter{})),
			codes.InvalidArgument,
		},
		{
			"no predicate",
			queryRequest("repo", retrieveNode(compareFilter(
				&cloudyneigh.Compare{Attribute: "id"},
			))),
			codes.InvalidArgument,
		},
		{
			"predicate without a value",
			queryRequest("repo", retrieveNode(compareFilter(&cloudyneigh.Compare{
				Attribute: "id",
				Predicate: &cloudyneigh.Compare_Eq{Eq: &cloudyneigh.Value{}},
			}))),
			codes.InvalidArgument,
		},
		{"empty id", queryRequest("repo", retrieveNode(compareFilter(idEquals("")))), codes.InvalidArgument},
		{
			"filter on another attribute",
			queryRequest("repo", retrieveNode(compareFilter(&cloudyneigh.Compare{
				Attribute: "language",
				Predicate: &cloudyneigh.Compare_Eq{
					Eq: &cloudyneigh.Value{Kind: &cloudyneigh.Value_Text{Text: "go"}},
				},
			}))),
			codes.Unimplemented,
		},
	}

	client := newClient(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := client.Query(t.Context(), tt.req)
			if got := status.Code(err); got != tt.want {
				t.Errorf("Query code = %s, want %s (error %v)", got, tt.want, err)
			}
		})
	}
}
