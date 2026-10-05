package dalgo2d1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/recordset"
	dalrecord "github.com/dal-go/record"
)

type queryRequest struct {
	Version    int           `json:"version"`
	Collection string        `json:"collection"`
	Columns    []string      `json:"columns,omitempty"`
	Filters    []queryFilter `json:"filters,omitempty"`
	Orders     []queryOrder  `json:"orders,omitempty"`
	Limit      int           `json:"limit,omitempty"`
	Offset     int           `json:"offset,omitempty"`
}

type queryFilter struct {
	Field    string `json:"field"`
	Operator string `json:"operator"`
	Value    any    `json:"value"`
}

type queryOrder struct {
	Field     string `json:"field"`
	Direction string `json:"direction"`
}

type queryResponse struct {
	Version    int              `json:"version"`
	Columns    []string         `json:"columns"`
	PrimaryKey []string         `json:"primaryKey"`
	Records    []map[string]any `json:"records"`
}

type projectionField struct {
	wireName string
	output   string
}

type compiledQuery struct {
	request    queryRequest
	collection string
	projection []projectionField
	allColumns bool
}

func (db *Database) compileQuery(query dal.Query) (compiledQuery, error) {
	structured, ok := query.(dal.StructuredQuery)
	if !ok || structured == nil {
		return compiledQuery{}, notSupported("query must be a structured DALgo query")
	}
	if structured.From() == nil || structured.From().Base() == nil {
		return compiledQuery{}, notSupported("query source is required")
	}
	if len(structured.From().Joins()) != 0 {
		return compiledQuery{}, notSupported("join must be evaluated by DALgo federation")
	}
	if len(structured.GroupBy()) != 0 || structured.Having() != nil {
		return compiledQuery{}, notSupported("group-by and having must be evaluated by DALgo federation")
	}
	if structured.StartFrom() != "" || structured.StartAfter() != "" {
		return compiledQuery{}, notSupported("DALgo cursors are not supported")
	}
	if structured.Offset() < 0 || structured.Offset() > maxSafeInteger {
		return compiledQuery{}, notSupported("offset is outside the Worker safe integer range")
	}
	if structured.Limit() < 0 || structured.Limit() > maxSafeInteger {
		return compiledQuery{}, notSupported("limit is outside the Worker safe integer range")
	}
	collectionRef, ok := asCollectionRef(structured.From().Base())
	if !ok {
		return compiledQuery{}, notSupported("only flat D1 collection references are supported")
	}
	if collectionRef.Schema() != "" || collectionRef.Parent() != nil {
		return compiledQuery{}, notSupported("qualified or parent-scoped collections are not supported")
	}
	if db.cfg.databaseID != "" && collectionRef.Database() != "" && collectionRef.Database() != db.cfg.databaseID {
		return compiledQuery{}, notSupported("query targets a different database")
	}
	if db.cfg.databaseID == "" && collectionRef.Database() != "" {
		return compiledQuery{}, notSupported("database-qualified references require WithDatabaseID")
	}
	collection := collectionRef.Name()
	schema, ok := db.schema[collection]
	if !ok {
		return compiledQuery{}, notSupported("collection is not allowlisted")
	}
	compiled := compiledQuery{
		collection: collection,
		request:    queryRequest{Version: protocolVersion, Collection: collection, Offset: structured.Offset(), Limit: structured.Limit()},
	}
	var err error
	compiled.request.Columns, compiled.projection, compiled.allColumns, err = queryProjection(structured, collectionRef, schema)
	if err != nil {
		return compiledQuery{}, err
	}
	filters, err := conditionToFilters(structured.Where(), collection, collectionRef, schema)
	if err != nil {
		return compiledQuery{}, err
	}
	compiled.request.Filters = filters
	if err := validateD1Filters(filters); err != nil {
		return compiledQuery{}, err
	}
	for _, order := range structured.OrderBy() {
		field, ok := order.Expression().(dal.FieldRef)
		if !ok || field.IsID() || field.Name() == "" || strings.Contains(field.Name(), ".") {
			return compiledQuery{}, notSupported("order-by must use direct field references")
		}
		if err := validateField(field, collection, collectionRef, schema); err != nil {
			return compiledQuery{}, err
		}
		direction := "asc"
		if order.Descending() {
			direction = "desc"
		}
		compiled.request.Orders = append(compiled.request.Orders, queryOrder{Field: field.Name(), Direction: direction})
	}
	return compiled, nil
}

func validateD1Filters(filters []queryFilter) error {
	boundValues := 0
	for _, filter := range filters {
		if filter.Operator == "in" || filter.Operator == "not-in" {
			values, ok := filter.Value.([]any)
			if !ok || len(values) == 0 || len(values) > 100 {
				return notSupported("IN filters require 1 to 100 values")
			}
			boundValues += len(values)
			continue
		}
		switch filter.Value.(type) {
		case nil:
		case bool, string, json.Number, float64, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, blobTag:
			boundValues++
		default:
			return notSupported("D1 filters require scalar values; arrays require IN or NOT IN")
		}
	}
	if boundValues > 100 {
		return notSupported("D1 queries support at most 100 bound filter values")
	}
	return nil
}

func asCollectionRef(source dal.RecordsetSource) (dal.CollectionRef, bool) {
	switch ref := source.(type) {
	case dal.CollectionRef:
		return ref, true
	case *dal.CollectionRef:
		if ref != nil {
			return *ref, true
		}
	}
	return dal.CollectionRef{}, false
}

func notSupported(reason string) error { return fmt.Errorf("%w: %s", dal.ErrNotSupported, reason) }

func queryProjection(q dal.StructuredQuery, ref dal.CollectionRef, schema CollectionSchema) ([]string, []projectionField, bool, error) {
	columns := q.Columns()
	if len(columns) == 0 {
		return nil, nil, true, nil
	}
	requested := make([]string, 0, len(columns))
	projection := make([]projectionField, 0, len(columns))
	seenWire, seenOutput := map[string]bool{}, map[string]bool{}
	for _, column := range columns {
		if column.Wildcard != nil {
			if column.Alias != "" {
				return nil, nil, false, notSupported("wildcard aliases are not supported")
			}
			if column.Wildcard.Source != "" && column.Wildcard.Source != ref.Name() && column.Wildcard.Source != ref.Alias() {
				return nil, nil, false, notSupported("wildcard projection source does not match query collection")
			}
			for _, name := range schema.Columns {
				if column.Wildcard.Excludes(name) || seenWire[name] {
					continue
				}
				if seenOutput[name] {
					return nil, nil, false, notSupported("wildcard projection conflicts with an existing alias")
				}
				seenWire[name] = true
				seenOutput[name] = true
				requested = append(requested, name)
				projection = append(projection, projectionField{wireName: name, output: name})
			}
			continue
		}
		field, ok := column.Expression.(dal.FieldRef)
		if !ok || field.IsID() || field.Name() == "" || strings.Contains(field.Name(), ".") {
			return nil, nil, false, notSupported("projection must use direct field references")
		}
		if err := validateField(field, ref.Name(), ref, schema); err != nil {
			return nil, nil, false, err
		}
		output := column.Alias
		if output == "" {
			output = field.Name()
		}
		if seenWire[field.Name()] || seenOutput[output] {
			return nil, nil, false, notSupported("projection contains duplicate fields or aliases")
		}
		seenWire[field.Name()] = true
		seenOutput[output] = true
		requested = append(requested, field.Name())
		projection = append(projection, projectionField{wireName: field.Name(), output: output})
	}
	if len(requested) == 0 {
		return nil, nil, false, notSupported("projection must select at least one field")
	}
	return requested, projection, false, nil
}

func validateField(field dal.FieldRef, collection string, ref dal.CollectionRef, schema CollectionSchema) error {
	if field.Source() != "" && field.Source() != collection && field.Source() != ref.Alias() {
		return notSupported("field qualifier does not match query collection")
	}
	for _, name := range schema.Columns {
		if name == field.Name() {
			return nil
		}
	}
	return notSupported("field is not allowlisted for collection")
}

func conditionToFilters(condition dal.Condition, collection string, ref dal.CollectionRef, schema CollectionSchema) ([]queryFilter, error) {
	if condition == nil {
		return nil, nil
	}
	switch c := condition.(type) {
	case dal.GroupCondition:
		if c.Operator() != dal.And {
			return nil, notSupported("only AND filter groups are supported")
		}
		var filters []queryFilter
		for _, child := range c.Conditions() {
			childFilters, err := conditionToFilters(child, collection, ref, schema)
			if err != nil {
				return nil, err
			}
			filters = append(filters, childFilters...)
		}
		return filters, nil
	case dal.IsNullCondition:
		field, ok := c.Operand().(dal.FieldRef)
		if !ok || field.IsID() {
			return nil, notSupported("null checks must use direct field references")
		}
		if err := validateField(field, collection, ref, schema); err != nil {
			return nil, err
		}
		op := "=="
		if c.Negated() {
			op = "!="
		}
		return []queryFilter{{Field: field.Name(), Operator: op, Value: nil}}, nil
	case dal.Comparison:
		field, ok := c.Left.(dal.FieldRef)
		if !ok || field.IsID() || field.Name() == "" || strings.Contains(field.Name(), ".") {
			return nil, notSupported("filters must have a direct field reference on the left")
		}
		if err := validateField(field, collection, ref, schema); err != nil {
			return nil, err
		}
		operator, err := comparisonOperator(c.Operator)
		if err != nil {
			return nil, err
		}
		var value any
		switch right := c.Right.(type) {
		case dal.Constant:
			value, err = toWireValue(right.Value, schema, field.Name())
		case dal.Array:
			if c.Operator != dal.In && c.Operator != dal.NotIn {
				return nil, notSupported("arrays require IN or NOT IN")
			}
			value, err = toWireArray(right.Value, schema, field.Name())
		default:
			return nil, notSupported("filters must compare a field to a constant")
		}
		if err != nil {
			return nil, err
		}
		return []queryFilter{{Field: field.Name(), Operator: operator, Value: value}}, nil
	default:
		return nil, notSupported("unsupported filter expression")
	}
}

func comparisonOperator(operator dal.Operator) (string, error) {
	switch operator {
	case dal.Equal:
		return "==", nil
	case dal.Operator("!="), dal.Operator("<>"), dal.Operator("NotEqual"):
		return "!=", nil
	case dal.LessThen:
		return "<", nil
	case dal.LessOrEqual:
		return "<=", nil
	case dal.GreaterThen:
		return ">", nil
	case dal.GreaterOrEqual:
		return ">=", nil
	case dal.In:
		return "in", nil
	case dal.NotIn:
		return "not-in", nil
	default:
		return "", notSupported("comparison operator is not supported")
	}
}

func toWireArray(value any, schema CollectionSchema, field string) ([]any, error) {
	rv := reflect.ValueOf(value)
	if !rv.IsValid() || rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array || rv.Type().Elem().Kind() == reflect.Uint8 {
		return nil, notSupported("IN values must be a non-empty array")
	}
	if rv.Len() == 0 {
		return nil, notSupported("empty IN arrays are not supported by the Worker protocol")
	}
	items := make([]any, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		item, err := toWireValue(rv.Index(i).Interface(), schema, field)
		if err != nil {
			return nil, err
		}
		items[i] = item
	}
	return items, nil
}

func toWireValue(value any, schema CollectionSchema, field string) (any, error) {
	if value == nil {
		return nil, nil
	}
	switch v := value.(type) {
	case []byte:
		if !contains(schema.BlobColumns, field) {
			return nil, notSupported("BLOB filter is not configured for this field")
		}
		return blobValue(v), nil
	case json.Number:
		return validateJSONNumber(v)
	case time.Time:
		return v.Format(time.RFC3339Nano), nil
	case json.RawMessage:
		var decoded any
		dec := json.NewDecoder(strings.NewReader(string(v)))
		dec.UseNumber()
		if err := dec.Decode(&decoded); err != nil {
			return nil, notSupported("raw JSON filter value is invalid")
		}
		if err := ensureJSONEOF(dec); err != nil {
			return nil, notSupported("raw JSON filter value contains trailing data")
		}
		return validateJSONValue(decoded)
	}
	return validateJSONValue(value)
}

func validateJSONValue(value any) (any, error) {
	switch v := value.(type) {
	case nil, bool, string:
		return v, nil
	case json.Number:
		return validateJSONNumber(v)
	case float32:
		number := float64(v)
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return nil, notSupported("non-finite numbers cannot be sent to the Worker")
		}
		if math.Trunc(number) == number && math.Abs(number) > maxSafeInteger {
			return nil, notSupported("integer exceeds the Worker safe integer range")
		}
		return number, nil
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, notSupported("non-finite numbers cannot be sent to the Worker")
		}
		if math.Trunc(v) == v && math.Abs(v) > maxSafeInteger {
			return nil, notSupported("integer exceeds the Worker safe integer range")
		}
		return v, nil
	case int:
		return safeSignedInteger(int64(v))
	case int8:
		return int64(v), nil
	case int16:
		return int64(v), nil
	case int32:
		return int64(v), nil
	case int64:
		return safeSignedInteger(v)
	case uint:
		return safeUnsignedInteger(uint64(v))
	case uint8:
		return uint64(v), nil
	case uint16:
		return uint64(v), nil
	case uint32:
		return uint64(v), nil
	case uint64:
		return safeUnsignedInteger(v)
	case []byte:
		return nil, notSupported("BLOB filters must use a configured field")
	case []any:
		items := make([]any, len(v))
		for i := range v {
			item, err := validateJSONValue(v[i])
			if err != nil {
				return nil, err
			}
			items[i] = item
		}
		return items, nil
	case map[string]any:
		mapped := make(map[string]any, len(v))
		for key, item := range v {
			checked, err := validateJSONValue(item)
			if err != nil {
				return nil, err
			}
			mapped[key] = checked
		}
		return mapped, nil
	}
	rv := reflect.ValueOf(value)
	if !rv.IsValid() {
		return nil, nil
	}
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		items := make([]any, rv.Len())
		for i := range items {
			checked, err := validateJSONValue(rv.Index(i).Interface())
			if err != nil {
				return nil, err
			}
			items[i] = checked
		}
		return items, nil
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			break
		}
		mapped := make(map[string]any, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			checked, err := validateJSONValue(iter.Value().Interface())
			if err != nil {
				return nil, err
			}
			mapped[iter.Key().String()] = checked
		}
		return mapped, nil
	}
	return nil, notSupported("filter value is not a JSON scalar, array, object, or BLOB")
}

func validateJSONNumber(value json.Number) (json.Number, error) {
	if _, err := value.Int64(); err == nil {
		integer, _ := value.Int64()
		if integer > maxSafeInteger || integer < -maxSafeInteger {
			return "", notSupported("integer exceeds the Worker safe integer range")
		}
		return value, nil
	}
	number, err := value.Float64()
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return "", notSupported("number is not finite or representable")
	}
	if math.Trunc(number) == number && math.Abs(number) > maxSafeInteger {
		return "", notSupported("integer exceeds the Worker safe integer range")
	}
	return value, nil
}

func safeSignedInteger(value int64) (int64, error) {
	if value > maxSafeInteger || value < -maxSafeInteger {
		return 0, notSupported("integer exceeds the Worker safe integer range")
	}
	return value, nil
}

func safeUnsignedInteger(value uint64) (uint64, error) {
	if value > maxSafeInteger {
		return 0, notSupported("integer exceeds the Worker safe integer range")
	}
	return value, nil
}

func contains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func (db *Database) fetchPage(ctx context.Context, base queryRequest, offset, limit int, schema CollectionSchema) (queryResponse, error) {
	base.Offset = offset
	base.Limit = limit
	var response queryResponse
	if err := db.request(ctx, "POST", "/v1/query", base, &response); err != nil {
		return queryResponse{}, err
	}
	if response.Version != protocolVersion {
		return queryResponse{}, fmt.Errorf("%w: unsupported query protocol version %d", ErrInvalidResponse, response.Version)
	}
	if response.Columns == nil || response.PrimaryKey == nil || response.Records == nil {
		return queryResponse{}, fmt.Errorf("%w: query columns, primaryKey, and records must be arrays", ErrInvalidResponse)
	}
	if !sameStrings(response.PrimaryKey, schema.PrimaryKey) {
		return queryResponse{}, fmt.Errorf("%w: primary key does not match allowlist", ErrInvalidResponse)
	}
	if err := validateResponseColumns(response.Columns, base.Columns, schema); err != nil {
		return queryResponse{}, err
	}
	if len(response.Records) > limit {
		return queryResponse{}, fmt.Errorf("%w: Worker returned more records than requested", ErrInvalidResponse)
	}
	for i, row := range response.Records {
		if err := validateResponseRow(row, response.Columns, schema); err != nil {
			return queryResponse{}, fmt.Errorf("%w: record %d: %v", ErrInvalidResponse, offset+i, err)
		}
	}
	return response, nil
}

func validateResponseColumns(actual, requested []string, schema CollectionSchema) error {
	allowed := make(map[string]bool, len(schema.Columns))
	for _, name := range schema.Columns {
		allowed[name] = true
	}
	seen := make(map[string]bool, len(actual))
	for _, name := range actual {
		if !allowed[name] || seen[name] {
			return fmt.Errorf("%w: unknown or duplicate response column %q", ErrInvalidResponse, name)
		}
		seen[name] = true
	}
	for _, key := range schema.PrimaryKey {
		if !seen[key] {
			return fmt.Errorf("%w: response omitted primary-key column %q", ErrInvalidResponse, key)
		}
	}
	if requested == nil && len(actual) != len(schema.Columns) {
		return fmt.Errorf("%w: response did not include every allowlisted column", ErrInvalidResponse)
	}
	for _, name := range requested {
		if !seen[name] {
			return fmt.Errorf("%w: response omitted requested column %q", ErrInvalidResponse, name)
		}
	}
	return nil
}

func validateResponseRow(row map[string]any, columns []string, schema CollectionSchema) error {
	if row == nil {
		return errors.New("record must be a JSON object")
	}
	allowed := make(map[string]bool, len(columns))
	for _, column := range columns {
		allowed[column] = true
	}
	for name := range row {
		if !allowed[name] {
			return fmt.Errorf("unexpected field %q", name)
		}
	}
	for _, column := range columns {
		value, ok := row[column]
		if !ok {
			return fmt.Errorf("missing field %q", column)
		}
		converted, err := fromWireValue(value, contains(schema.BlobColumns, column))
		if err != nil {
			return fmt.Errorf("field %q: %w", column, err)
		}
		row[column] = converted
	}
	return nil
}

func (db *Database) ExecuteQueryToRecordsReader(ctx context.Context, query dal.Query) (dal.RecordsReader, error) {
	compiled, err := db.compileQuery(query)
	if err != nil {
		return nil, err
	}
	limit := compiled.request.Limit
	if limit > db.cfg.maxRows {
		return nil, ErrResultTooLarge
	}
	baseOffset := compiled.request.Offset
	if limit == 0 || limit > db.cfg.pageSize {
		scanColumns := db.schema[compiled.collection].PrimaryKey
		if len(scanColumns) == 0 {
			scanColumns = db.schema[compiled.collection].Columns
		}
		for _, column := range scanColumns {
			if !hasOrderField(compiled.request.Orders, column) {
				compiled.request.Orders = append(compiled.request.Orders, queryOrder{Field: column, Direction: "asc"})
			}
		}
	}
	result := make([]map[string]any, 0)
	for {
		pageLimit := db.cfg.pageSize
		if limit > 0 && limit-len(result) < pageLimit {
			pageLimit = limit - len(result)
		}
		if limit == 0 && db.cfg.maxRows-len(result) < pageLimit {
			pageLimit = db.cfg.maxRows - len(result) + 1
		}
		if pageLimit <= 0 {
			break
		}
		if baseOffset > maxSafeInteger-len(result) {
			return nil, notSupported("paged offset exceeds the Worker safe integer range")
		}
		page, err := db.fetchPage(ctx, compiled.request, baseOffset+len(result), pageLimit, db.schema[compiled.collection])
		if err != nil {
			return nil, err
		}
		if limit == 0 && len(result)+len(page.Records) > db.cfg.maxRows {
			return nil, ErrResultTooLarge
		}
		result = append(result, page.Records...)
		if len(page.Records) < pageLimit || limit > 0 && len(result) >= limit {
			break
		}
	}
	return makeRecordsReader(compiled, query.(dal.StructuredQuery), result, db.schema[compiled.collection])
}

func hasOrderField(orders []queryOrder, field string) bool {
	for _, order := range orders {
		if order.Field == field {
			return true
		}
	}
	return false
}

func (db *Database) ExecuteQueryToRecordsetReader(ctx context.Context, query dal.Query, options ...recordset.Option) (dal.RecordsetReader, error) {
	compiled, err := db.compileQuery(query)
	if err != nil {
		return nil, err
	}
	structured := query.(dal.StructuredQuery)
	reader, err := db.ExecuteQueryToRecordsReader(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	columns := make([]string, 0, len(compiled.projection))
	if compiled.allColumns {
		columns = append(columns, db.schema[compiled.collection].Columns...)
	} else {
		for _, column := range compiled.projection {
			columns = append(columns, column.output)
		}
	}
	if len(columns) == 0 {
		return nil, notSupported("recordset projection must select at least one field")
	}
	definitions := make([]recordset.Column[any], len(columns))
	for i, name := range columns {
		definitions[i] = recordset.NewTypedColumn[any](name, nil)
	}
	name := structured.From().Base().Name()
	if configured := recordset.NewOptions(options...).Name(); configured != "" {
		name = configured
	}
	result := recordset.NewColumnarRecordset(name, definitions...)
	for {
		item, nextErr := reader.Next()
		if errors.Is(nextErr, dal.ErrNoMoreRecords) {
			break
		}
		if nextErr != nil {
			return nil, nextErr
		}
		data, mapErr := dalrecord.DataToMap(item.Data())
		if mapErr != nil {
			return nil, fmt.Errorf("dalgo2d1: read recordset data: %w", mapErr)
		}
		row := result.NewRow()
		for _, column := range columns {
			if setErr := row.SetValueByName(column, data[column], result); setErr != nil {
				return nil, setErr
			}
		}
	}
	return &recordsetReader{recordset: result}, nil
}

var _ dal.ReadSession = (*Database)(nil)
var _ dal.QueryExecutor = (*Database)(nil)
