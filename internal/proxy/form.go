package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"mime/multipart"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/swayam5342/plugit/internal/model"
)

// Body content types plugit can send.
const (
	ctJSON      = "application/json"
	ctForm      = "application/x-www-form-urlencoded"
	ctMultipart = "multipart/form-data"
)

// encodeBody serializes the body arguments for the operation's content
// type (SPEC M6 design note: form and multipart bodies). It returns the
// body and its Content-Type, or nil when there is no body.
func encodeBody(b *model.Body, fields map[string]any, root any, hasRoot bool) ([]byte, string, error) {
	if !hasRoot && len(fields) == 0 {
		return nil, "", nil
	}
	ct := ctJSON
	if b != nil && b.ContentType != "" {
		ct = b.ContentType
	}
	if ct != ctForm && ct != ctMultipart {
		v := any(fields)
		if hasRoot {
			v = root
		}
		data, err := json.Marshal(v)
		if err != nil {
			return nil, "", fmt.Errorf("%w: encoding body: %w", ErrBadArgument, err)
		}
		return data, ctJSON, nil
	}

	obj := fields
	if hasRoot {
		m, ok := root.(map[string]any)
		if !ok {
			return nil, "", fmt.Errorf("%w: a %s body must be an object", ErrBadArgument, ct)
		}
		obj = m
	}
	if ct == ctMultipart {
		return multipartBody(obj)
	}
	vals := url.Values{}
	for _, k := range slices.Sorted(maps.Keys(obj)) {
		enc, explicit := b.Encoding[k]
		if err := formField(vals, k, obj[k], enc, explicit); err != nil {
			return nil, "", err
		}
	}
	return []byte(vals.Encode()), ctForm, nil
}

// formScalar formats a scalar for a form; ok is false for objects, arrays
// and null.
func formScalar(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case bool:
		return strconv.FormatBool(x), true
	case json.Number:
		return x.String(), true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	case int:
		return strconv.Itoa(x), true
	case int64:
		return strconv.FormatInt(x, 10), true
	}
	return "", false
}

// formField adds one top-level field. Without an explicit encoding the
// OpenAPI default applies: style form, explode true.
func formField(vals url.Values, key string, v any, enc model.Encoding, explicit bool) error {
	if v == nil {
		return nil
	}
	if s, ok := formScalar(v); ok {
		vals.Add(key, s)
		return nil
	}
	if enc.Style == "deepObject" {
		return deepField(vals, key, v)
	}
	explode := !explicit || enc.Explode
	switch x := v.(type) {
	case []any:
		if !explode {
			parts, ok := scalarList(x)
			if !ok {
				return deepField(vals, key, v)
			}
			vals.Add(key, strings.Join(parts, ","))
			return nil
		}
		parts, ok := scalarList(x)
		if !ok {
			// Objects can't be repeated keys: the whole array goes in
			// brackets, so its indexes stay consistent.
			return deepField(vals, key, v)
		}
		for _, s := range parts {
			vals.Add(key, s)
		}
		return nil
	case map[string]any:
		if !explode {
			var parts []string
			for _, k := range slices.Sorted(maps.Keys(x)) {
				s, ok := formScalar(x[k])
				if !ok {
					return deepField(vals, key, v)
				}
				parts = append(parts, k, s)
			}
			vals.Add(key, strings.Join(parts, ","))
			return nil
		}
		for _, k := range slices.Sorted(maps.Keys(x)) {
			if s, ok := formScalar(x[k]); ok {
				vals.Add(k, s)
			} else if err := deepField(vals, k, x[k]); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("%w: field %q has a value a form can't carry", ErrBadArgument, key)
}

func scalarList(xs []any) ([]string, bool) {
	out := make([]string, 0, len(xs))
	for _, e := range xs {
		s, ok := formScalar(e)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// deepField writes v under bracket keys, prefix[k] for objects and
// prefix[i] for arrays, recursively.
func deepField(vals url.Values, prefix string, v any) error {
	switch x := v.(type) {
	case nil:
		return nil
	case map[string]any:
		for _, k := range slices.Sorted(maps.Keys(x)) {
			if err := deepField(vals, prefix+"["+k+"]", x[k]); err != nil {
				return err
			}
		}
		return nil
	case []any:
		for i, e := range x {
			if err := deepField(vals, fmt.Sprintf("%s[%d]", prefix, i), e); err != nil {
				return err
			}
		}
		return nil
	}
	s, ok := formScalar(v)
	if !ok {
		return fmt.Errorf("%w: field %q has a value a form can't carry", ErrBadArgument, prefix)
	}
	vals.Add(prefix, s)
	return nil
}

// multipartBody sends text fields: scalars as text, arrays as repeated
// fields, objects (and objects in arrays) as JSON text.
func multipartBody(obj map[string]any) ([]byte, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	write := func(k string, v any) error {
		if s, ok := formScalar(v); ok {
			return w.WriteField(k, s)
		}
		data, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("%w: field %q: %w", ErrBadArgument, k, err)
		}
		return w.WriteField(k, string(data))
	}
	for _, k := range slices.Sorted(maps.Keys(obj)) {
		switch x := obj[k].(type) {
		case nil:
		case []any:
			for _, e := range x {
				if err := write(k, e); err != nil {
					return nil, "", err
				}
			}
		default:
			if err := write(k, x); err != nil {
				return nil, "", err
			}
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}
