package dalgo2d1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/recordset"
	dalrecord "github.com/dal-go/record"
)

func (db *Database) Get(ctx context.Context, target dalrecord.Record) error {
	if target == nil || target.Key() == nil {
		return errors.New("dalgo2d1: Get requires a record with a key")
	}
	key := target.Key()
	schema, ok := db.schema[key.Collection()]
	if !ok {
		err := notSupported("collection is not allowlisted")
		target.SetError(err)
		return err
	}
	filters, err := filtersForKey(key, schema)
	if err != nil {
		target.SetError(err)
		return err
	}
	request := queryRequest{Version: protocolVersion, Collection: key.Collection(), Filters: filters}
	page, err := db.fetchPage(ctx, request, 0, 1, schema)
	if err != nil {
		target.SetError(err)
		return err
	}
	if len(page.Records) == 0 {
		err := dal.NewErrNotFoundByKey(key, nil)
		target.SetError(err)
		return err
	}
	if len(page.Records) != 1 {
		err := fmt.Errorf("%w: Get returned multiple records for one key", ErrInvalidResponse)
		target.SetError(err)
		return err
	}
	target.SetError(nil)
	data := target.Data()
	if data == nil {
		err := errors.New("dalgo2d1: Get requires a record data target")
		target.SetError(err)
		return err
	}
	if err := dalrecord.MapToData(data, page.Records[0]); err != nil {
		err = fmt.Errorf("dalgo2d1: populate Get target: %w", err)
		target.SetError(err)
		return err
	}
	return nil
}

func (db *Database) Exists(ctx context.Context, key *dalrecord.Key) (bool, error) {
	if key == nil {
		return false, errors.New("dalgo2d1: Exists requires a key")
	}
	schema, ok := db.schema[key.Collection()]
	if !ok {
		return false, notSupported("collection is not allowlisted")
	}
	filters, err := filtersForKey(key, schema)
	if err != nil {
		return false, err
	}
	request := queryRequest{Version: protocolVersion, Collection: key.Collection(), Filters: filters}
	page, err := db.fetchPage(ctx, request, 0, 1, schema)
	if err != nil {
		return false, err
	}
	if len(page.Records) > 1 {
		return false, fmt.Errorf("%w: Exists returned multiple records for one key", ErrInvalidResponse)
	}
	return len(page.Records) == 1, nil
}

func (db *Database) GetMulti(ctx context.Context, targets []dalrecord.Record) error {
	for _, target := range targets {
		if err := db.Get(ctx, target); err != nil && !dalrecord.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func filtersForKey(key *dalrecord.Key, schema CollectionSchema) ([]queryFilter, error) {
	if key.Parent() != nil {
		return nil, notSupported("parent-scoped keys are not supported")
	}
	if len(schema.PrimaryKey) == 0 {
		return nil, notSupported("Get and Exists require a primary key")
	}
	values := make(map[string]any, len(schema.PrimaryKey))
	if fields, ok := key.ID.([]dalrecord.FieldVal); ok {
		for _, field := range fields {
			if _, exists := values[field.Name]; exists {
				return nil, notSupported("composite key repeats a field")
			}
			values[field.Name] = field.Value
		}
		if len(values) != len(schema.PrimaryKey) {
			return nil, notSupported("composite key must provide every primary-key field")
		}
	} else if len(schema.PrimaryKey) == 1 {
		values[schema.PrimaryKey[0]] = key.ID
	} else {
		return nil, notSupported("composite keys must use record.FieldVal entries")
	}
	filters := make([]queryFilter, 0, len(schema.PrimaryKey))
	for _, field := range schema.PrimaryKey {
		value, ok := values[field]
		if !ok || value == nil {
			return nil, notSupported("key is missing a primary-key value")
		}
		encoded, err := toWireValue(value, schema, field)
		if err != nil {
			return nil, err
		}
		filters = append(filters, queryFilter{Field: field, Operator: "==", Value: encoded})
	}
	for field := range values {
		if !contains(schema.PrimaryKey, field) {
			return nil, notSupported("composite key includes a non-primary-key field")
		}
	}
	return filters, nil
}

func makeRecordsReader(compiled compiledQuery, query dal.StructuredQuery, rows []map[string]any, schema CollectionSchema) (dal.RecordsReader, error) {
	records := make([]dalrecord.Record, 0, len(rows))
	for _, row := range rows {
		data := projectRow(row, compiled.projection, compiled.allColumns, schema)
		var result dalrecord.Record
		if intoRecord := query.IntoRecord(); intoRecord != nil {
			result = intoRecord
			result.SetError(nil)
			target := result.Data()
			if target == nil {
				return nil, errors.New("dalgo2d1: query IntoRecord returned no data target")
			}
			if err := dalrecord.MapToData(target, data); err != nil {
				return nil, fmt.Errorf("dalgo2d1: populate query record: %w", err)
			}
			if len(schema.PrimaryKey) != 0 && result.Key() != nil {
				key, err := keyFromRow(compiled.collection, row, schema)
				if err != nil {
					return nil, err
				}
				result.Key().ID = key.ID
				result.Key().IDKind = key.IDKind
			}
		} else if len(schema.PrimaryKey) != 0 {
			key, err := keyFromRow(compiled.collection, row, schema)
			if err != nil {
				return nil, err
			}
			result = dalrecord.NewRecordWithData(key, data)
		} else {
			result = dalrecord.NewRecordWithoutKey(data)
		}
		records = append(records, result)
	}
	return dal.NewRecordsReader(records), nil
}

func projectRow(row map[string]any, projection []projectionField, allColumns bool, schema CollectionSchema) map[string]any {
	if allColumns {
		return cloneRow(row)
	}
	result := make(map[string]any, len(projection))
	for _, field := range projection {
		result[field.output] = row[field.wireName]
	}
	return result
}

func cloneRow(row map[string]any) map[string]any {
	copy := make(map[string]any, len(row))
	for key, value := range row {
		copy[key] = value
	}
	return copy
}

func keyFromRow(collection string, row map[string]any, schema CollectionSchema) (*dalrecord.Key, error) {
	if len(schema.PrimaryKey) == 0 {
		return nil, notSupported("keyless collections do not produce keyed records")
	}
	if len(schema.PrimaryKey) == 1 {
		value, ok := row[schema.PrimaryKey[0]]
		if !ok || value == nil {
			return nil, fmt.Errorf("%w: primary-key value is missing", ErrInvalidResponse)
		}
		return dalrecord.NewKeyWithID(collection, comparableKeyValue(value)), nil
	}
	fields := make([]dalrecord.FieldVal, 0, len(schema.PrimaryKey))
	for _, name := range schema.PrimaryKey {
		value, ok := row[name]
		if !ok || value == nil {
			return nil, fmt.Errorf("%w: primary-key value %q is missing", ErrInvalidResponse, name)
		}
		fields = append(fields, dalrecord.FieldVal{Name: name, Value: comparableKeyValue(value)})
	}
	return dalrecord.NewKeyWithFields(collection, fields...), nil
}

type recordsetReader struct {
	recordset recordset.Recordset
	index     int
	closed    bool
}

func (r *recordsetReader) Recordset() recordset.Recordset { return r.recordset }

func (r *recordsetReader) Next() (recordset.Row, recordset.Recordset, error) {
	if r.closed {
		return nil, r.recordset, dal.ErrNoMoreRecords
	}
	row := r.recordset.GetRow(r.index)
	if row == nil {
		return nil, r.recordset, dal.ErrNoMoreRecords
	}
	r.index++
	return row, r.recordset, nil
}

func (r *recordsetReader) Cursor() (string, error) {
	if r.closed {
		return "", dal.ErrNoMoreRecords
	}
	return strconv.Itoa(r.index), nil
}

func (r *recordsetReader) Close() error {
	r.closed = true
	return nil
}

func comparableKeyValue(value any) any {
	if number, ok := value.(json.Number); ok {
		if integer, err := number.Int64(); err == nil {
			return integer
		}
	}
	return value
}
