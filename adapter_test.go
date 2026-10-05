package dalgo2d1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dtql"
	dalrecord "github.com/dal-go/record"
)

func testSchema() Schema {
	return Schema{
		"Order Details": {Columns: []string{"OrderID", "ProductID", "UnitPrice", "Quantity", "Picture"}, PrimaryKey: []string{"OrderID", "ProductID"}, BlobColumns: []string{"Picture"}},
		"Summary View":  {Columns: []string{"Name", "Value"}},
		"Customer":      {Columns: []string{"id", "name"}, PrimaryKey: []string{"id"}},
		"Invoice":       {Columns: []string{"id", "country_id", "amount"}, PrimaryKey: []string{"id"}},
		"Country":       {Columns: []string{"id", "population"}, PrimaryKey: []string{"id"}},
	}
}

func newTestDB(t *testing.T, handler http.Handler, options ...Option) (*Database, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	db, err := New(server.URL, testSchema(), append([]Option{WithInsecureLoopbackForTesting()}, options...)...)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return db, server
}

func jsonResponse(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func TestGetCompositeKeyBlobNullAndProjection(t *testing.T) {
	var request queryRequest
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/query" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if request.Collection != "Order Details" || len(request.Filters) != 2 {
			t.Errorf("request=%+v", request)
		}
		jsonResponse(w, queryResponse{Version: 1, Columns: []string{"ProductID", "OrderID", "UnitPrice", "Quantity", "Picture"}, PrimaryKey: []string{"OrderID", "ProductID"}, Records: []map[string]any{{"ProductID": json.Number("7"), "OrderID": json.Number("41"), "UnitPrice": nil, "Quantity": json.Number("3"), "Picture": map[string]any{"$type": "blob", "base64": "AAEC/w=="}}}})
	})
	db, server := newTestDB(t, handler)
	defer server.Close()
	target := dalrecord.NewRecordWithData(dalrecord.NewKeyWithFields("Order Details", dalrecord.FieldVal{Name: "ProductID", Value: 7}, dalrecord.FieldVal{Name: "OrderID", Value: 41}), map[string]any{})
	if err := db.Get(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	got := target.Data().(map[string]any)
	if got["UnitPrice"] != nil || !reflect.DeepEqual(got["Picture"], []byte{0, 1, 2, 255}) {
		t.Fatalf("data=%#v", got)
	}
	if request.Filters[0].Field != "OrderID" || request.Filters[1].Field != "ProductID" {
		t.Fatalf("key filters not in configured PK order: %+v", request.Filters)
	}

	called := false
	keyless, keylessServer := newTestDB(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called = true }))
	defer keylessServer.Close()
	if err := keyless.Get(context.Background(), dalrecord.NewRecordWithData(dalrecord.NewKeyWithID("Summary View", "x"), map[string]any{})); err == nil || !errors.Is(err, dal.ErrNotSupported) {
		t.Fatalf("keyless Get err=%v", err)
	}
	if called {
		t.Fatal("keyless Get sent HTTP")
	}
}

func TestStructuredProjectionFiltersOrdersAndPagingDTO(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req queryRequest
		decoder := json.NewDecoder(r.Body)
		decoder.UseNumber()
		if err := decoder.Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req.Collection != "Customer" || !reflect.DeepEqual(req.Columns, []string{"name"}) {
			t.Errorf("request=%+v", req)
		}
		if len(req.Filters) != 1 || req.Filters[0].Field != "id" || req.Filters[0].Operator != ">=" || req.Filters[0].Value != json.Number("4") {
			t.Errorf("filters=%+v", req.Filters)
		}
		if len(req.Orders) != 1 || req.Orders[0] != (queryOrder{Field: "name", Direction: "desc"}) {
			t.Errorf("orders=%+v", req.Orders)
		}
		if req.Limit != 1 || req.Offset != 2 {
			t.Errorf("limit/offset=%d/%d", req.Limit, req.Offset)
		}
		jsonResponse(w, queryResponse{Version: 1, Columns: []string{"name", "id"}, PrimaryKey: []string{"id"}, Records: []map[string]any{{"name": "Ada", "id": json.Number("7")}}})
	})
	db, server := newTestDB(t, handler)
	defer server.Close()
	query := dal.From(dal.NewRootCollectionRef("Customer", "")).NewQuery().Where(dal.NewComparison(dal.Field("id"), dal.GreaterOrEqual, dal.NewConstant(4))).OrderBy(dal.DescendingField("name")).Offset(2).Limit(1).SelectColumns(dal.Column{Expression: dal.Field("name"), Alias: "display"})
	reader, err := db.ExecuteQueryToRecordsReader(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	row, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if got := row.Data().(map[string]any); len(got) != 1 || got["display"] != "Ada" {
		t.Fatalf("projected row=%#v", got)
	}
}

func TestQueryPagingRecordsetAndRowBudget(t *testing.T) {
	rows := []map[string]any{{"id": json.Number("1"), "name": "a"}, {"id": json.Number("2"), "name": nil}, {"id": json.Number("3"), "name": "c"}}
	var offsets []int
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req queryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("unused"))
		_ = offset
		offsets = append(offsets, req.Offset)
		start, end := req.Offset, req.Offset+req.Limit
		if start > len(rows) {
			start = len(rows)
		}
		if end > len(rows) {
			end = len(rows)
		}
		jsonResponse(w, queryResponse{Version: 1, Columns: []string{"id", "name"}, PrimaryKey: []string{"id"}, Records: rows[start:end]})
	})
	db, server := newTestDB(t, handler, WithPageSize(2), WithMaxRows(4))
	defer server.Close()
	q := dal.From(dal.NewRootCollectionRef("Customer", "")).NewQuery().SelectIntoRecord(nil)
	rs, err := db.ExecuteQueryToRecordsetReader(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if rs.Recordset().RowsCount() != 3 {
		t.Fatalf("rows=%d", rs.Recordset().RowsCount())
	}
	if !reflect.DeepEqual(offsets, []int{0, 2}) {
		t.Fatalf("offsets=%v", offsets)
	}
	if row, _, err := rs.Next(); err != nil || row == nil {
		t.Fatalf("next row=%v err=%v", row, err)
	}

	limited, limitServer := newTestDB(t, handler, WithPageSize(2), WithMaxRows(2))
	defer limitServer.Close()
	_, err = limited.ExecuteQueryToRecordsReader(context.Background(), q)
	if !errors.Is(err, ErrResultTooLarge) {
		t.Fatalf("over-budget err=%v", err)
	}
	_, err = limited.ExecuteQueryToRecordsReader(context.Background(), dal.From(dal.NewRootCollectionRef("Customer", "")).NewQuery().Limit(2).SelectIntoRecord(nil))
	if err != nil {
		t.Fatalf("explicit limit err=%v", err)
	}
}

func TestKeylessQueryAndRecordsetRemainReadable(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req queryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req.Collection != "Summary View" {
			t.Errorf("collection=%q", req.Collection)
		}
		jsonResponse(w, queryResponse{Version: 1, Columns: []string{"Name", "Value"}, PrimaryKey: []string{}, Records: []map[string]any{{"Name": "total", "Value": nil}}})
	})
	db, server := newTestDB(t, handler)
	defer server.Close()
	query := dal.From(dal.NewRootCollectionRef("Summary View", "")).NewQuery().SelectIntoRecord(nil)
	reader, err := db.ExecuteQueryToRecordsReader(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	record, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if record.Key() != nil {
		t.Fatalf("keyless view got key %v", record.Key())
	}
	if record.Data().(map[string]any)["Value"] != nil {
		t.Fatalf("NULL was not preserved: %v", record.Data())
	}
	rs, err := db.ExecuteQueryToRecordsetReader(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if rs.Recordset().RowsCount() != 1 {
		t.Fatalf("recordset rows=%d", rs.Recordset().RowsCount())
	}
	row, _, err := rs.Next()
	if err != nil {
		t.Fatal(err)
	}
	value, err := row.GetValueByName("Value", rs.Recordset())
	if err != nil || value != nil {
		t.Fatalf("keyless recordset NULL=%#v err=%v", value, err)
	}
}

func TestWorkerBindBudgetAndPageSizeBounds(t *testing.T) {
	filters := make([]queryFilter, 101)
	for i := range filters {
		filters[i] = queryFilter{Field: "id", Operator: "==", Value: i}
	}
	if err := validateD1Filters(filters); err == nil || !errors.Is(err, dal.ErrNotSupported) {
		t.Fatalf("filter budget err=%v", err)
	}
	if _, err := New("https://worker.example", testSchema(), WithPageSize(maxWorkerQueryLimit+1)); err == nil {
		t.Fatal("accepted page size above Worker maximum")
	}
}

func TestParentKeysAliasCollisionAndTrailingJSONFailClosed(t *testing.T) {
	calls := 0
	db, server := newTestDB(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; jsonResponse(w, map[string]any{}) }))
	defer server.Close()
	parent := dalrecord.NewKeyWithID("Parent", "p")
	child := dalrecord.NewKeyWithParentAndID(parent, "Customer", 1)
	if err := db.Get(context.Background(), dalrecord.NewRecordWithData(child, map[string]any{})); err == nil || !errors.Is(err, dal.ErrNotSupported) {
		t.Fatalf("parent Get err=%v", err)
	}
	if _, err := db.Exists(context.Background(), child); err == nil || !errors.Is(err, dal.ErrNotSupported) {
		t.Fatalf("parent Exists err=%v", err)
	}
	query := dal.From(dal.NewRootCollectionRef("Customer", "")).NewQuery().SelectColumns(
		dal.Column{Expression: dal.Field("id"), Alias: "name"},
		dal.Column{Wildcard: &dal.WildcardProjection{}},
	)
	if _, err := db.compileQuery(query); err == nil || !errors.Is(err, dal.ErrNotSupported) {
		t.Fatalf("wildcard alias collision err=%v", err)
	}
	if _, err := toWireValue(json.RawMessage(`{"ok":1} {"extra":2}`), CollectionSchema{}, "field"); err == nil || !errors.Is(err, dal.ErrNotSupported) {
		t.Fatalf("trailing JSON err=%v", err)
	}
	if calls != 0 {
		t.Fatalf("fail-closed cases made %d HTTP calls", calls)
	}
}

func TestUnsupportedShapeAndInvalidJSONFailBeforeHTTP(t *testing.T) {
	calls := 0
	db, server := newTestDB(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; jsonResponse(w, map[string]any{}) }))
	defer server.Close()
	q := dal.From(dal.NewRootCollectionRef("Customer", "")).NewQuery().Where(dal.NewComparison(dal.Field("id"), dal.Equal, dal.NewConstant(int64(1<<53)))).SelectIntoRecord(nil)
	_, err := db.ExecuteQueryToRecordsReader(context.Background(), q)
	if err == nil || !errors.Is(err, dal.ErrNotSupported) {
		t.Fatalf("unsafe query err=%v", err)
	}
	join := dal.From(dal.NewRootCollectionRef("Customer", "")).Join(dal.NewJoinedSource(dal.NewRootCollectionRef("Country", "c"), dal.JoinInner)).NewQuery().SelectIntoRecord(nil)
	_, err = db.ExecuteQueryToRecordsReader(context.Background(), join)
	if err == nil || !errors.Is(err, dal.ErrNotSupported) {
		t.Fatalf("join err=%v", err)
	}
	if calls != 0 {
		t.Fatalf("unsupported query made %d HTTP calls", calls)
	}
	if _, err = New("https://user:pass@example.test", testSchema()); err == nil {
		t.Fatal("accepted URL credentials")
	}
}

func TestVersionPinAndRedirectErrors(t *testing.T) {
	var paths []string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Header.Get("X-Dalgo-Schema-Version") != "schema-1" || r.Header.Get("X-Dalgo-Seed-Version") != "seed-1" {
			t.Errorf("missing version headers")
		}
		w.WriteHeader(http.StatusConflict)
		jsonResponse(w, map[string]any{"version": 1, "error": map[string]any{"code": "version_mismatch", "message": "deployment version mismatch"}})
	})
	db, server := newTestDB(t, handler, WithExpectedSchemaVersion("schema-1"), WithExpectedSeedVersion("seed-1"))
	defer server.Close()
	_, err := db.Metadata(context.Background())
	if !errors.Is(err, ErrVersionMismatch) || errors.Is(err, dal.ErrNotSupported) {
		t.Fatalf("metadata err=%v", err)
	}
	_, err = db.ExecuteQueryToRecordsReader(context.Background(), dal.From(dal.NewRootCollectionRef("Customer", "")).NewQuery().SelectIntoRecord(nil))
	if !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("query err=%v", err)
	}
	if !reflect.DeepEqual(paths, []string{"/v1/metadata", "/v1/query"}) {
		t.Fatalf("paths=%v", paths)
	}

	redirect, redirectServer := newTestDB(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.invalid", http.StatusTemporaryRedirect)
	}))
	defer redirectServer.Close()
	_, err = redirect.Metadata(context.Background())
	if err == nil || !strings.Contains(err.Error(), "redirects are not allowed") {
		t.Fatalf("redirect err=%v", err)
	}
}

func TestDALgoFederatedJoinAndAggregateOverPagedHTTPLeaves(t *testing.T) {
	orders := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req queryRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		all := []map[string]any{{"id": json.Number("1"), "country_id": json.Number("5"), "amount": json.Number("12")}, {"id": json.Number("2"), "country_id": json.Number("5"), "amount": json.Number("8")}}
		start, end := req.Offset, req.Offset+req.Limit
		if start > len(all) {
			start = len(all)
		}
		if end > len(all) {
			end = len(all)
		}
		jsonResponse(w, queryResponse{Version: 1, Columns: []string{"id", "country_id", "amount"}, PrimaryKey: []string{"id"}, Records: all[start:end]})
	})
	countries := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req queryRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		all := []map[string]any{{"id": json.Number("5"), "population": json.Number("100")}}
		start, end := req.Offset, req.Offset+req.Limit
		if start > len(all) {
			start = len(all)
		}
		if end > len(all) {
			end = len(all)
		}
		jsonResponse(w, queryResponse{Version: 1, Columns: []string{"id", "population"}, PrimaryKey: []string{"id"}, Records: all[start:end]})
	})
	left, leftServer := newTestDB(t, orders, WithPageSize(1), WithDatabaseID("orders"))
	defer leftServer.Close()
	right, rightServer := newTestDB(t, countries, WithPageSize(1), WithDatabaseID("countries"))
	defer rightServer.Close()
	const doc = `from:
  database: orders
  name: Invoice
  alias: o
  joins:
    - from: {database: countries, name: Country, alias: c}
      on: [{left: {field: country_id, source: o}, op: '==', right: {field: id, source: c}}]
groupBy: [{field: id, source: c}]
columns:
  - {field: id, source: c, as: countryId}
  - {aggregate: {function: sum, args: [{field: amount, source: o}]}, as: totalSales}
`
	q, err := dtql.Deserialize([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	reader, err := dal.ExecuteFederatedQuery(context.Background(), q, func(_ context.Context, dbName string) (dal.QueryExecutor, error) {
		switch dbName {
		case "orders":
			return left, nil
		case "countries":
			return right, nil
		default:
			return nil, fmt.Errorf("unexpected db %s", dbName)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := dal.ReadAllToRecords(context.Background(), reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("rows=%d", len(got))
	}
	data := got[0].Data().(map[string]any)
	if fmt.Sprint(data["totalSales"]) != "20" {
		t.Fatalf("aggregate data=%#v", data)
	}
}
