package dalgo2d1

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
)

type blobTag struct {
	Type   string `json:"$type"`
	Base64 string `json:"base64"`
}

func blobValue(value []byte) blobTag {
	return blobTag{Type: "blob", Base64: base64.StdEncoding.EncodeToString(value)}
}

func fromWireValue(value any, blobAllowed bool) (any, error) {
	switch v := value.(type) {
	case nil, bool, string:
		return v, nil
	case json.Number:
		return validateResponseNumber(v)
	case []any:
		items := make([]any, len(v))
		for i := range v {
			checked, err := fromWireValue(v[i], false)
			if err != nil {
				return nil, err
			}
			items[i] = checked
		}
		return items, nil
	case map[string]any:
		if marker, tagged := v["$type"]; tagged {
			if !blobAllowed || marker != "blob" || len(v) != 2 {
				return nil, fmt.Errorf("%w: unknown or unexpected typed value", ErrInvalidResponse)
			}
			encoded, ok := v["base64"].(string)
			if !ok {
				return nil, fmt.Errorf("%w: BLOB tag must contain base64 text", ErrInvalidResponse)
			}
			data, err := base64.StdEncoding.Strict().DecodeString(encoded)
			if err != nil {
				return nil, fmt.Errorf("%w: invalid BLOB base64", ErrInvalidResponse)
			}
			return data, nil
		}
		object := make(map[string]any, len(v))
		for key, item := range v {
			checked, err := fromWireValue(item, false)
			if err != nil {
				return nil, err
			}
			object[key] = checked
		}
		return object, nil
	default:
		return nil, fmt.Errorf("%w: unexpected JSON value type %T", ErrInvalidResponse, value)
	}
}

func validateResponseNumber(value json.Number) (json.Number, error) {
	if integer, err := value.Int64(); err == nil {
		if integer > maxSafeInteger || integer < -maxSafeInteger {
			return "", fmt.Errorf("%w: integer exceeds JavaScript safe range", ErrInvalidResponse)
		}
		return value, nil
	}
	number, err := value.Float64()
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return "", fmt.Errorf("%w: number is non-finite or invalid", ErrInvalidResponse)
	}
	if math.Trunc(number) == number && math.Abs(number) > maxSafeInteger {
		return "", fmt.Errorf("%w: integer exceeds JavaScript safe range", ErrInvalidResponse)
	}
	return value, nil
}
