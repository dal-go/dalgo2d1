package dalgo2d1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dalgotest"
	dalrecord "github.com/dal-go/record"
)

// The rule every read keeps: within one read, no two records have the same key (a record
// with no key, which dalgotest.HasNoKey says, may repeat). These tests hold every read
// path to it with dalgotest.AssertUniqueKeys, over a fake of the D1 HTTP API serving
// tables whose non-key values repeat.

const ukInvoiceRows = 412

func ukSchema() Schema {
	return Schema{
		"Invoice":     {Columns: []string{"InvoiceId", "BillingCountry", "Total"}, PrimaryKey: []string{"InvoiceId"}},
		"CompositePk": {Columns: []string{"Region", "Code", "Label"}, PrimaryKey: []string{"Region", "Code"}},
		"NoPk":        {Columns: []string{"Name", "Qty"}},
		"InvoiceView": {Columns: []string{"BillingCountry", "Total"}},
	}
}

func ukTables() map[string][]map[string]any {
	invoices := make([]map[string]any, ukInvoiceRows)
	for i := range invoices {
		invoices[i] = map[string]any{"InvoiceId": json.Number(fmt.Sprint(i + 1)), "BillingCountry": []string{"USA", "France", "Brazil"}[i%3], "Total": json.Number("0.99")}
	}
	view := make([]map[string]any, ukInvoiceRows)
	for i := range view {
		view[i] = map[string]any{"BillingCountry": "USA", "Total": json.Number("0.99")}
	}
	return map[string][]map[string]any{
		"Invoice": invoices,
		"CompositePk": {
			{"Region": "eu", "Code": json.Number("1"), "Label": "same"}, {"Region": "eu", "Code": json.Number("2"), "Label": "same"},
			{"Region": "us", "Code": json.Number("1"), "Label": "same"}, {"Region": "us", "Code": json.Number("2"), "Label": "same"},
		},
		"NoPk":        {{"Name": "bolt", "Qty": json.Number("10")}, {"Name": "bolt", "Qty": json.Number("10")}, {"Name": "nut", "Qty": json.Number("10")}},
		"InvoiceView": view,
	}
}

// ukHandler is a fake of the D1 HTTP API: it pages the tables and projects columns.
func ukHandler(schema Schema, tables map[string][]map[string]any) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req queryRequest
		decoder := json.NewDecoder(r.Body)
		decoder.UseNumber()
		if err := decoder.Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		collection := schema[req.Collection]
		columns := collection.Columns
		if req.Columns != nil {
			columns = nil
			for _, name := range collection.Columns {
				if contains(req.Columns, name) || contains(collection.PrimaryKey, name) {
					columns = append(columns, name)
				}
			}
		}
		rows := tables[req.Collection]
		start := min(req.Offset, len(rows))
		end := min(start+req.Limit, len(rows))
		page := make([]map[string]any, 0, end-start)
		for _, row := range rows[start:end] {
			projected := make(map[string]any, len(columns))
			for _, name := range columns {
				projected[name] = row[name]
			}
			page = append(page, projected)
		}
		jsonResponse(w, queryResponse{Version: 1, Columns: columns, PrimaryKey: append([]string{}, collection.PrimaryKey...), Records: page})
	})
}

func ukReadAll(t *testing.T, db *Database, q dal.Query) ([]dalrecord.Record, error) {
	t.Helper()
	reader, err := db.ExecuteQueryToRecordsReader(context.Background(), q)
	if err != nil {
		return nil, err
	}
	var records []dalrecord.Record
	for {
		r, err := reader.Next()
		if errors.Is(err, dal.ErrNoMoreRecords) {
			return records, nil
		}
		if err != nil {
			return nil, err
		}
		records = append(records, r)
	}
}

func ukFrom(name string) dal.FromSource { return dal.From(dal.NewRootCollectionRef(name, "")) }

func ukSelect(name string, columns ...string) dal.Query {
	cols := make([]dal.Column, len(columns))
	for i, c := range columns {
		cols[i] = dal.Column{Expression: dal.Field(c)}
	}
	return ukFrom(name).NewQuery().SelectColumns(cols...)
}

func TestEveryReadKeepsItsKeysUniqueWithinTheRead(t *testing.T) {
	schema := ukSchema()
	server := httptest.NewServer(ukHandler(schema, ukTables()))
	defer server.Close()
	db, err := New(server.URL, schema, WithInsecureLoopbackForTesting())
	if err != nil {
		t.Fatal(err)
	}
	nameKeyless := func(t *testing.T, got []dalrecord.Record) {
		for i, r := range got {
			if !dalgotest.HasNoKey(r.Key()) {
				t.Fatalf("record %d of a keyless source has the key %v", i, r.Key())
			}
		}
	}
	keyedBy := func(column string) func(*testing.T, []dalrecord.Record) {
		return func(t *testing.T, got []dalrecord.Record) {
			for i, r := range got {
				if want := got[i].Data().(map[string]any)[column]; r.Key().ID != want && fmt.Sprint(r.Key().ID) != fmt.Sprint(want) {
					t.Fatalf("record %d has the key %v, want %v", i, r.Key().ID, want)
				}
			}
		}
	}
	intoRecord := func(name string) dal.Query {
		return ukFrom(name).NewQuery().SelectIntoRecord(func() dalrecord.Record {
			return dalrecord.NewRecordWithData(dalrecord.NewKeyWithID(name, "made-by-the-caller"), map[string]any{})
		})
	}
	for _, tc := range []struct {
		name  string
		query dal.Query
		rows  int
		check func(*testing.T, []dalrecord.Record)
	}{
		{"select-all of a single-column primary key", ukSelect("Invoice"), ukInvoiceRows, keyedBy("InvoiceId")},
		{"select columns without the key", ukSelect("Invoice", "BillingCountry", "Total"), ukInvoiceRows, nil},
		{"select columns with the key", ukSelect("Invoice", "InvoiceId", "BillingCountry"), ukInvoiceRows, keyedBy("InvoiceId")},
		{"limit and offset", ukFrom("Invoice").NewQuery().Offset(5).Limit(20).SelectColumns(), 20, nil},
		{"a composite primary key, select-all", ukSelect("CompositePk"), 4, nil},
		{"a composite primary key, select columns", ukSelect("CompositePk", "Label"), 4, nil},
		{"no primary key, rows all alike", ukSelect("NoPk"), 3, nameKeyless},
		{"no primary key, select columns", ukSelect("NoPk", "Name"), 3, nameKeyless},
		{"a view", ukSelect("InvoiceView"), ukInvoiceRows, nameKeyless},
		{"into a caller's record, keyed source", intoRecord("Invoice"), ukInvoiceRows, keyedBy("InvoiceId")},
		{"into a caller's record, composite source", intoRecord("CompositePk"), 4, nil},
		{"into a caller's record, keyless source", intoRecord("NoPk"), 3, nameKeyless},
		{"into a caller's record, view", intoRecord("InvoiceView"), ukInvoiceRows, nameKeyless},
	} {
		t.Run(tc.name, func(t *testing.T) {
			records, err := ukReadAll(t, db, tc.query)
			if err != nil {
				t.Fatal(err)
			}
			if len(records) != tc.rows {
				t.Fatalf("read %d records, want %d", len(records), tc.rows)
			}
			dalgotest.AssertUniqueKeys(t, records)
			if tc.check != nil {
				tc.check(t, records)
			}
		})
	}

	// A GROUP BY and a join are not evaluated by D1: the read fails and returns no records.
	groupBy := ukFrom("Invoice").NewQuery().GroupBy(dal.Field("BillingCountry")).SelectColumns(dal.Column{Expression: dal.Field("BillingCountry")}, dal.Count())
	join := ukFrom("Invoice").Join(dal.NewJoinedSource(dal.NewRootCollectionRef("CompositePk", "c"), dal.JoinInner)).NewQuery().SelectColumns()
	for name, q := range map[string]dal.Query{"a GROUP BY": groupBy, "a join": join} {
		if _, err := ukReadAll(t, db, q); !errors.Is(err, dal.ErrNotSupported) {
			t.Fatalf("%s: err=%v, want ErrNotSupported", name, err)
		}
	}
}

func TestTheRecordsetReadKeepsEveryRow(t *testing.T) {
	schema := ukSchema()
	server := httptest.NewServer(ukHandler(schema, ukTables()))
	defer server.Close()
	db, err := New(server.URL, schema, WithInsecureLoopbackForTesting())
	if err != nil {
		t.Fatal(err)
	}
	rs, err := db.ExecuteQueryToRecordsetReader(context.Background(), ukSelect("InvoiceView"))
	if err != nil {
		t.Fatal(err)
	}
	if got := rs.Recordset().RowsCount(); got != ukInvoiceRows {
		t.Fatalf("rows=%d", got)
	}
}

// A response that repeats a primary key is invalid: it would hand back records that share a key.
func TestAResponseRepeatingAPrimaryKeyIsInvalid(t *testing.T) {
	schema := ukSchema()
	for _, name := range []string{"Invoice", "CompositePk"} {
		tables := ukTables()
		tables[name] = append(tables[name], tables[name][0])
		server := httptest.NewServer(ukHandler(schema, tables))
		db, err := New(server.URL, schema, WithInsecureLoopbackForTesting())
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		_, err = ukReadAll(t, db, ukSelect(name))
		server.Close()
		if !errors.Is(err, ErrInvalidResponse) {
			t.Fatalf("%s: err=%v, want ErrInvalidResponse", name, err)
		}
	}
}
