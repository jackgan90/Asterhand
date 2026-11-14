package automation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackgan90/Asterhand/internal/localcommands"
)

// Decode rejects unknown fields, trailing JSON, duplicate keys, invalid Unicode
// and missing required fields, identically for CLI, MCP and the local transport.
func Decode[T any](raw json.RawMessage) (T, error) {
	var result T
	if len(raw) > MaxRequest {
		return result, Fail("request_too_large", "Request exceeds 1 MiB")
	}
	if err := checkJSON(raw); err != nil {
		return result, err
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return result, Fail("invalid_parameters", "Invalid JSON")
	}
	if err := requiredFields(value, reflect.TypeFor[T](), "arguments"); err != nil {
		return result, err
	}
	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, Fail("invalid_parameters", "Unknown field or incorrect value type")
	}
	return result, nil
}

func requiredFields(value any, typ reflect.Type, field string) error {
	if typ == reflect.TypeFor[json.RawMessage]() || typ.Kind() == reflect.Interface {
		return nil
	}
	if typ.Kind() == reflect.Pointer {
		if value == nil {
			return nil
		}
		return requiredFields(value, typ.Elem(), field)
	}
	if value == nil {
		return &Error{Code: "invalid_parameters", Message: "Value cannot be null", Field: field}
	}
	if typ.Kind() == reflect.Struct {
		obj, ok := value.(map[string]any)
		if !ok {
			return Fail("invalid_parameters", "Expected an object")
		}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if f.Anonymous && f.Type.Kind() == reflect.Struct && f.Tag.Get("json") == "" {
				if err := requiredFields(value, f.Type, field); err != nil {
					return err
				}
				continue
			}
			name, optional := jsonField(f)
			if name == "" {
				continue
			}
			v, found := obj[name]
			if !found && !optional {
				return &Error{Code: "invalid_parameters", Message: "Required field is missing", Field: field + "." + name}
			}
			if found {
				if err := requiredFields(v, f.Type, field+"."+name); err != nil {
					return err
				}
			}
		}
	}
	if typ.Kind() == reflect.Slice {
		if items, ok := value.([]any); ok {
			for i, item := range items {
				if err := requiredFields(item, typ.Elem(), fmt.Sprintf("%s[%d]", field, i)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func jsonField(f reflect.StructField) (string, bool) {
	if !f.IsExported() {
		return "", true
	}
	parts := strings.Split(f.Tag.Get("json"), ",")
	if parts[0] == "-" {
		return "", true
	}
	if parts[0] == "" {
		parts[0] = f.Name
	}
	return parts[0], strings.Contains(f.Tag.Get("json"), ",omitempty")
}

func Schema[T any]() map[string]any { return typeSchema(reflect.TypeFor[T]()) }

func typeSchema(typ reflect.Type) map[string]any {
	if typ == reflect.TypeFor[localcommands.NotificationSetting]() {
		return map[string]any{"type": "boolean"}
	}
	if typ == reflect.TypeFor[[]byte]() {
		return map[string]any{"type": "string", "contentEncoding": "base64"}
	}
	if typ == reflect.TypeFor[time.Time]() {
		return map[string]any{"type": "string", "format": "date-time"}
	}
	if typ == reflect.TypeFor[json.RawMessage]() || typ.Kind() == reflect.Interface {
		return map[string]any{}
	}
	if typ.Kind() == reflect.Pointer {
		return map[string]any{"anyOf": []any{typeSchema(typ.Elem()), map[string]any{"type": "null"}}}
	}
	s := map[string]any{}
	switch typ.Kind() {
	case reflect.Struct:
		props := map[string]any{}
		required := []string{}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if f.Anonymous && f.Type.Kind() == reflect.Struct && f.Tag.Get("json") == "" {
				embedded := typeSchema(f.Type)
				for key, value := range embedded["properties"].(map[string]any) {
					props[key] = value
				}
				required = append(required, embedded["required"].([]string)...)
				continue
			}
			name, optional := jsonField(f)
			if name == "" {
				continue
			}
			props[name] = typeSchema(f.Type)
			if description := f.Tag.Get("description"); description != "" {
				props[name].(map[string]any)["description"] = description
			}
			if !optional {
				required = append(required, name)
			}
		}
		s["type"], s["properties"], s["required"], s["additionalProperties"] = "object", props, required, false
	case reflect.String:
		s["type"] = "string"
	case reflect.Bool:
		s["type"] = "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint32, reflect.Uint64:
		s["type"] = "integer"
	case reflect.Float32, reflect.Float64:
		s["type"] = "number"
	case reflect.Slice, reflect.Array:
		s["type"], s["items"] = "array", typeSchema(typ.Elem())
	case reflect.Map:
		s["type"], s["additionalProperties"] = "object", typeSchema(typ.Elem())
	}
	return s
}

func checkJSON(raw []byte) error {
	if !json.Valid(raw) || !utf8.Valid(raw) || !validEscapedUnicode(raw) {
		return Fail("invalid_parameters", "Invalid JSON")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := uniqueJSON(d, 0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return Fail("invalid_parameters", "Trailing JSON")
	}
	return nil
}

// encoding/json replaces lone UTF-16 surrogates with U+FFFD. Reject those
// sequences before decoding so the saved text and intent digest cannot differ
// from what the sender actually supplied.
func validEscapedUnicode(raw []byte) bool {
	inString := false
	for i := 0; i < len(raw); i++ {
		if raw[i] == '"' {
			inString = !inString
			continue
		}
		if !inString || raw[i] != '\\' {
			continue
		}
		i++
		if raw[i] != 'u' {
			continue
		}
		value, _ := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		i += 4
		if value >= 0xDC00 && value <= 0xDFFF {
			return false
		}
		if value < 0xD800 || value > 0xDBFF {
			continue
		}
		if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
			return false
		}
		low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
		if err != nil || low < 0xDC00 || low > 0xDFFF {
			return false
		}
		i += 6
	}
	return true
}

func uniqueJSON(d *json.Decoder, depth int) error {
	if depth > 64 {
		return Fail("invalid_parameters", "JSON nesting exceeds 64 levels")
	}
	token, err := d.Token()
	if err != nil {
		return Fail("invalid_parameters", "Invalid JSON")
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	if delim == '{' {
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return Fail("invalid_parameters", "Invalid JSON")
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return Fail("invalid_parameters", "Duplicate JSON field")
			}
			seen[name] = true
			if err = uniqueJSON(d, depth+1); err != nil {
				return err
			}
		}
	} else if delim == '[' {
		for d.More() {
			if err = uniqueJSON(d, depth+1); err != nil {
				return err
			}
		}
	}
	_, err = d.Token()
	return err
}
