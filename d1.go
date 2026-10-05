// Package dalgo2d1 provides a read-only DALgo adapter for Cloudflare D1's
// typed Worker query protocol. It never accepts or sends SQL text.
package dalgo2d1

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const protocolVersion = 1
const maxSafeInteger = 1<<53 - 1
const maxWorkerQueryLimit = 1000

var (
	ErrVersionMismatch = errors.New("dalgo2d1: deployment version mismatch")
	ErrResultTooLarge  = errors.New("dalgo2d1: query result exceeds configured row budget")
	ErrInvalidResponse = errors.New("dalgo2d1: invalid Worker response")
)

// CollectionSchema is the client-side allowlist for one D1 collection.
// PrimaryKey is ordered; an empty key describes a keyless view.
type CollectionSchema struct {
	Columns     []string
	PrimaryKey  []string
	BlobColumns []string
}

// Schema is the caller's explicit allowlist of collections and fields.
type Schema map[string]CollectionSchema

// Metadata is returned by GET /v1/metadata. It identifies the immutable
// schema and seed behind a Worker deployment when the operator provides pins.
type Metadata struct {
	Version       int              `json:"version"`
	SchemaVersion string           `json:"schemaVersion,omitempty"`
	SeedVersion   string           `json:"seedVersion,omitempty"`
	Collections   []CollectionInfo `json:"collections"`
}

// CollectionInfo describes one configured Worker collection.
type CollectionInfo struct {
	Name       string   `json:"name"`
	Columns    []string `json:"columns"`
	PrimaryKey []string `json:"primaryKey"`
}

// Option configures a Database.
type Option func(*config)

type config struct {
	client                *http.Client
	bearerToken           string
	expectedSchema        string
	expectedSeed          string
	databaseID            string
	pageSize              int
	maxRows               int
	maxResponseBytes      int64
	allowInsecureLoopback bool
}

// WithHTTPClient uses client transport settings while enforcing the adapter's
// no-redirect policy. A 20 second timeout is added when client has none.
func WithHTTPClient(client *http.Client) Option {
	return func(c *config) { c.client = client }
}

// WithBearerToken sends token only in the Authorization header.
func WithBearerToken(token string) Option {
	return func(c *config) { c.bearerToken = token }
}

// WithExpectedSchemaVersion pins requests to a Worker's schema version.
func WithExpectedSchemaVersion(version string) Option {
	return func(c *config) { c.expectedSchema = version }
}

// WithExpectedSeedVersion pins requests to a Worker's immutable seed version.
func WithExpectedSeedVersion(version string) Option {
	return func(c *config) { c.expectedSeed = version }
}

// WithDatabaseID requires DALgo database-qualified references to match id.
func WithDatabaseID(id string) Option { return func(c *config) { c.databaseID = id } }

// WithPageSize controls transparent Worker-side paging for DALgo scans. It
// must not exceed the Worker's configured maxQueryLimit (default 100).
func WithPageSize(size int) Option { return func(c *config) { c.pageSize = size } }

// WithMaxRows bounds the total rows retained by a query. Oversized results
// fail without returning a truncated reader.
func WithMaxRows(rows int) Option { return func(c *config) { c.maxRows = rows } }

// WithMaxResponseBytes bounds each HTTP response body.
func WithMaxResponseBytes(bytes int64) Option { return func(c *config) { c.maxResponseBytes = bytes } }

// WithInsecureLoopbackForTesting explicitly permits plain HTTP to localhost
// and loopback IPs. It must not be used for remote endpoints.
func WithInsecureLoopbackForTesting() Option {
	return func(c *config) { c.allowInsecureLoopback = true }
}

// Database implements dal.ReadSession and dal.QueryExecutor. It is read-only;
// it does not implement DALgo transactions or mutation interfaces.
type Database struct {
	baseURL *url.URL
	schema  Schema
	cfg     config
	client  *http.Client
}

// New creates a read-only D1 adapter for the explicitly allowlisted schema.
// HTTPS is required except for loopback URLs used by local tests.
func New(baseURL string, schema Schema, options ...Option) (*Database, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("dalgo2d1: base URL must be an absolute HTTPS or loopback HTTP URL without credentials, query, or fragment")
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !isLoopbackHost(u.Hostname())) {
		return nil, errors.New("dalgo2d1: HTTPS is required except for loopback HTTP")
	}
	if schema == nil {
		return nil, errors.New("dalgo2d1: schema allowlist is required")
	}
	copySchema, err := cloneAndValidateSchema(schema)
	if err != nil {
		return nil, err
	}
	c := config{pageSize: 100, maxRows: 10000, maxResponseBytes: 16 << 20}
	for _, option := range options {
		if option != nil {
			option(&c)
		}
	}
	if u.Scheme == "http" && !c.allowInsecureLoopback {
		return nil, errors.New("dalgo2d1: loopback HTTP requires WithInsecureLoopbackForTesting")
	}
	if c.pageSize <= 0 || c.pageSize > maxWorkerQueryLimit {
		return nil, fmt.Errorf("dalgo2d1: page size must be from 1 to %d", maxWorkerQueryLimit)
	}
	if c.maxRows <= 0 || c.maxRows > maxSafeInteger {
		return nil, errors.New("dalgo2d1: maximum rows must be positive")
	}
	if c.maxResponseBytes <= 0 {
		return nil, errors.New("dalgo2d1: maximum response bytes must be positive")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if u.Path == "" {
		u.Path = "/"
	}
	baseClient := c.client
	if baseClient == nil {
		baseClient = &http.Client{}
	}
	clientCopy := *baseClient
	if clientCopy.Timeout <= 0 {
		clientCopy.Timeout = 20 * time.Second
	}
	clientCopy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Database{baseURL: u, schema: copySchema, cfg: c, client: &clientCopy}, nil
}

func isLoopbackHost(host string) bool {
	host = strings.ToLower(host)
	if host == "localhost" {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func cloneAndValidateSchema(in Schema) (Schema, error) {
	out := make(Schema, len(in))
	for name, collection := range in {
		if strings.TrimSpace(name) == "" || len(collection.Columns) == 0 {
			return nil, fmt.Errorf("dalgo2d1: collection %q must have a name and at least one column", name)
		}
		cols := make(map[string]bool, len(collection.Columns))
		for _, col := range collection.Columns {
			if strings.TrimSpace(col) == "" || cols[col] {
				return nil, fmt.Errorf("dalgo2d1: collection %q has an empty or duplicate column %q", name, col)
			}
			cols[col] = true
		}
		seenPK := make(map[string]bool, len(collection.PrimaryKey))
		for _, key := range collection.PrimaryKey {
			if !cols[key] || seenPK[key] {
				return nil, fmt.Errorf("dalgo2d1: collection %q has invalid primary-key column %q", name, key)
			}
			seenPK[key] = true
		}
		seenBlob := make(map[string]bool, len(collection.BlobColumns))
		for _, col := range collection.BlobColumns {
			if !cols[col] || seenBlob[col] {
				return nil, fmt.Errorf("dalgo2d1: collection %q has invalid BLOB column %q", name, col)
			}
			seenBlob[col] = true
		}
		out[name] = CollectionSchema{
			Columns:     append([]string(nil), collection.Columns...),
			PrimaryKey:  append([]string(nil), collection.PrimaryKey...),
			BlobColumns: append([]string(nil), collection.BlobColumns...),
		}
	}
	if len(out) == 0 {
		return nil, errors.New("dalgo2d1: schema allowlist is empty")
	}
	return out, nil
}
