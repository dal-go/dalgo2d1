package dalgo2d1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type errorEnvelope struct {
	Version int `json:"version"`
	Error   struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// HTTPError reports only the Worker's status and stable error code. The remote
// message is intentionally omitted so query values and implementation details
// never leak into logs through an error string.
type HTTPError struct {
	Status int
	Code   string
}

func (e *HTTPError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("dalgo2d1: Worker returned HTTP %d", e.Status)
	}
	return fmt.Sprintf("dalgo2d1: Worker returned HTTP %d (%s)", e.Status, e.Code)
}

func (e *HTTPError) Is(target error) bool {
	return target == ErrVersionMismatch && e.Status == http.StatusConflict && e.Code == "version_mismatch"
}

func (db *Database) endpoint(route string) string {
	base := strings.TrimRight(db.baseURL.String(), "/")
	return base + "/" + strings.TrimLeft(route, "/")
}

func (db *Database) request(ctx context.Context, method, route string, body any, result any) error {
	var requestBody io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("dalgo2d1: encode request: %w", err)
		}
		requestBody = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, db.endpoint(route), requestBody)
	if err != nil {
		return fmt.Errorf("dalgo2d1: create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if db.cfg.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+db.cfg.bearerToken)
	}
	if db.cfg.expectedSchema != "" {
		req.Header.Set("X-Dalgo-Schema-Version", db.cfg.expectedSchema)
	}
	if db.cfg.expectedSeed != "" {
		req.Header.Set("X-Dalgo-Seed-Version", db.cfg.expectedSeed)
	}
	resp, err := db.client.Do(req)
	if err != nil {
		return fmt.Errorf("dalgo2d1: Worker request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, db.cfg.maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("dalgo2d1: read Worker response: %w", err)
	}
	if int64(len(data)) > db.cfg.maxResponseBytes {
		return fmt.Errorf("%w: response body exceeds %d bytes", ErrInvalidResponse, db.cfg.maxResponseBytes)
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return fmt.Errorf("dalgo2d1: redirects are not allowed (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var envelope errorEnvelope
		if err := json.Unmarshal(data, &envelope); err != nil || envelope.Version != protocolVersion {
			return &HTTPError{Status: resp.StatusCode}
		}
		return &HTTPError{Status: resp.StatusCode, Code: safeErrorCode(envelope.Error.Code)}
	}
	if result == nil {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	if err := dec.Decode(result); err != nil {
		return fmt.Errorf("%w: decode response envelope: %v", ErrInvalidResponse, err)
	}
	if err := ensureJSONEOF(dec); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidResponse, err)
	}
	return nil
}

func safeErrorCode(code string) string {
	if code == "" || len(code) > 80 {
		return ""
	}
	for _, r := range code {
		allowed := r >= 'a' && r <= 'z'
		allowed = allowed || r >= 'A' && r <= 'Z'
		allowed = allowed || r >= '0' && r <= '9'
		allowed = allowed || r == '_' || r == '-'
		if !allowed {
			return ""
		}
	}
	return code
}

func ensureJSONEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

type metadataResponse struct {
	Metadata
}

// Metadata fetches and validates GET /v1/metadata against the configured
// client-side schema allowlist.
func (db *Database) Metadata(ctx context.Context) (Metadata, error) {
	var response metadataResponse
	if err := db.request(ctx, http.MethodGet, "/v1/metadata", nil, &response); err != nil {
		return Metadata{}, err
	}
	if response.Version != protocolVersion {
		return Metadata{}, fmt.Errorf("%w: unsupported metadata protocol version %d", ErrInvalidResponse, response.Version)
	}
	if response.Collections == nil {
		return Metadata{}, fmt.Errorf("%w: metadata collections must be an array", ErrInvalidResponse)
	}
	if db.cfg.expectedSchema != "" && response.SchemaVersion != db.cfg.expectedSchema || db.cfg.expectedSeed != "" && response.SeedVersion != db.cfg.expectedSeed {
		return Metadata{}, ErrVersionMismatch
	}
	seen := make(map[string]bool, len(response.Collections))
	remote := make(map[string]CollectionInfo, len(response.Collections))
	for _, collection := range response.Collections {
		if collection.Name == "" || seen[collection.Name] {
			return Metadata{}, fmt.Errorf("%w: empty or duplicate collection name", ErrInvalidResponse)
		}
		seen[collection.Name] = true
		remote[collection.Name] = collection
	}
	for name, local := range db.schema {
		actual, ok := remote[name]
		if !ok {
			return Metadata{}, fmt.Errorf("%w: allowlisted collection %q is missing", ErrInvalidResponse, name)
		}
		if !sameStrings(actual.Columns, local.Columns) || !sameStrings(actual.PrimaryKey, local.PrimaryKey) {
			return Metadata{}, fmt.Errorf("%w: collection %q schema does not match allowlist", ErrInvalidResponse, name)
		}
	}
	return response.Metadata, nil
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
