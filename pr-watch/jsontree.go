package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// jsonObject is a JSON object that keeps the order of its keys, so a file pr-watch adds to, like Claude Code's
// settings, reads as it did around what it added. Its values are *jsonObject, []any, string, json.Number, bool or
// nil.
type jsonObject struct {
	keys []string
	vals map[string]any
}

func newJSONObject() *jsonObject { return &jsonObject{vals: map[string]any{}} }

func jsonNumber(n int) json.Number { return json.Number(strconv.Itoa(n)) }

func (o *jsonObject) get(key string) (any, bool) {
	v, ok := o.vals[key]
	return v, ok
}

// set replaces a key's value where it is, or adds the key last.
func (o *jsonObject) set(key string, v any) {
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = v
}

// object is the object under key, made when there's none.
func (o *jsonObject) object(key string) (*jsonObject, error) {
	v, ok := o.get(key)
	if !ok {
		child := newJSONObject()
		o.set(key, child)
		return child, nil
	}
	child, ok := v.(*jsonObject)
	if !ok {
		return nil, fmt.Errorf("%q isn't an object", key)
	}
	return child, nil
}

func (o *jsonObject) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		for _, v := range []any{k, o.vals[k]} {
			enc, err := marshalJSON(v)
			if err != nil {
				return nil, err
			}
			b.Write(enc)
			if v == k {
				b.WriteByte(':')
			}
		}
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// marshalJSON is json.Marshal leaving <, > and & as they are, as the files pr-watch edits have them.
func marshalJSON(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// encodeJSONFile is a JSON file the way Claude Code writes its settings: two spaces of indent, a newline at the end.
func encodeJSONFile(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// decodeJSON reads a JSON document keeping the order of its objects' keys; numbers stay as they're written.
func decodeJSON(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	v, err := decodeJSONValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("more after the JSON value")
	}
	return v, nil
}

func decodeJSONValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch tok {
	case json.Delim('{'):
		o := newJSONObject()
		for dec.More() {
			k, err := dec.Token()
			if err != nil {
				return nil, err
			}
			v, err := decodeJSONValue(dec)
			if err != nil {
				return nil, err
			}
			o.set(k.(string), v)
		}
		_, err := dec.Token() // }
		return o, err
	case json.Delim('['):
		a := []any{}
		for dec.More() {
			v, err := decodeJSONValue(dec)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
		}
		_, err := dec.Token() // ]
		return a, err
	}
	return tok, nil
}
