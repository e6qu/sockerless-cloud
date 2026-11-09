package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// cwtObj is a JSON object that keeps its keys in insertion order, so a
// transformed event reads in the order its processors built it. Values are
// *cwtObj, []any, string, json.Number, bool or nil.
type cwtObj struct {
	keys []string
	m    map[string]any
}

func newCWTObj() *cwtObj { return &cwtObj{m: map[string]any{}} }

func (o *cwtObj) Get(k string) (any, bool) {
	v, ok := o.m[k]
	return v, ok
}

func (o *cwtObj) Put(k string, v any) {
	if _, ok := o.m[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.m[k] = v
}

func (o *cwtObj) Delete(k string) (any, bool) {
	v, ok := o.m[k]
	if !ok {
		return nil, false
	}
	delete(o.m, k)
	for i, key := range o.keys {
		if key == k {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
	return v, true
}

func cwtSplitPath(path string) []string { return strings.Split(path, ".") }

func (o *cwtObj) GetPath(path string) (any, bool) {
	cur := o
	parts := cwtSplitPath(path)
	for i, p := range parts {
		v, ok := cur.Get(p)
		if !ok {
			return nil, false
		}
		if i == len(parts)-1 {
			return v, true
		}
		next, isObj := v.(*cwtObj)
		if !isObj {
			return nil, false
		}
		cur = next
	}
	return nil, false
}

// SetPath creates the intermediate objects a dotted path needs. It reports
// false when a value already sits at the path and overwrite is off, or when an
// intermediate key holds something other than an object.
func (o *cwtObj) SetPath(path string, v any, overwrite bool) bool {
	cur := o
	parts := cwtSplitPath(path)
	for _, p := range parts[:len(parts)-1] {
		existing, ok := cur.Get(p)
		if !ok {
			child := newCWTObj()
			cur.Put(p, child)
			cur = child
			continue
		}
		child, isObj := existing.(*cwtObj)
		if !isObj {
			return false
		}
		cur = child
	}
	last := parts[len(parts)-1]
	if _, exists := cur.Get(last); exists && !overwrite {
		return false
	}
	cur.Put(last, v)
	return true
}

func (o *cwtObj) DeletePath(path string) (any, bool) {
	parts := cwtSplitPath(path)
	parentPath := strings.Join(parts[:len(parts)-1], ".")
	parent := o
	if parentPath != "" {
		v, ok := o.GetPath(parentPath)
		if !ok {
			return nil, false
		}
		p, isObj := v.(*cwtObj)
		if !isObj {
			return nil, false
		}
		parent = p
	}
	return parent.Delete(parts[len(parts)-1])
}

func cwtDeepCopy(v any) any {
	switch t := v.(type) {
	case *cwtObj:
		c := newCWTObj()
		for _, k := range t.keys {
			c.Put(k, cwtDeepCopy(t.m[k]))
		}
		return c
	case []any:
		c := make([]any, len(t))
		for i, e := range t {
			c[i] = cwtDeepCopy(e)
		}
		return c
	default:
		return v
	}
}

func cwtCountLeaves(v any) int {
	switch t := v.(type) {
	case *cwtObj:
		n := 0
		for _, k := range t.keys {
			n += cwtCountLeaves(t.m[k])
		}
		return n
	default:
		return 1
	}
}

var errCWTTrailingData = errors.New("unexpected data after the JSON value")

func cwtDecode(s string) (any, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	v, err := cwtDecodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errCWTTrailingData
	}
	return v, nil
}

func cwtDecodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := newCWTObj()
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := kt.(string)
				if !ok {
					return nil, fmt.Errorf("object key is not a string")
				}
				val, err := cwtDecodeValue(dec)
				if err != nil {
					return nil, err
				}
				obj.Put(key, val)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return obj, nil
		case '[':
			arr := []any{}
			for dec.More() {
				val, err := cwtDecodeValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, val)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return arr, nil
		}
		return nil, fmt.Errorf("unexpected delimiter %q", t)
	default:
		return t, nil
	}
}

// cwtEncode writes JSON without HTML escaping, as the service's encoder does.
func cwtEncode(v any) string {
	var b bytes.Buffer
	cwtWrite(&b, v)
	return b.String()
}

func cwtWrite(b *bytes.Buffer, v any) {
	switch t := v.(type) {
	case *cwtObj:
		b.WriteByte('{')
		for i, k := range t.keys {
			if i > 0 {
				b.WriteByte(',')
			}
			cwtWriteString(b, k)
			b.WriteByte(':')
			cwtWrite(b, t.m[k])
		}
		b.WriteByte('}')
	case []any:
		b.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			cwtWrite(b, e)
		}
		b.WriteByte(']')
	case string:
		cwtWriteString(b, t)
	case json.Number:
		b.WriteString(t.String())
	case bool:
		if t {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case nil:
		b.WriteString("null")
	default:
		panic(fmt.Sprintf("cwtWrite: unexpected %T", v))
	}
}

func cwtWriteString(b *bytes.Buffer, s string) {
	var sb bytes.Buffer
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		panic(err)
	}
	b.Write(bytes.TrimSuffix(sb.Bytes(), []byte{'\n'}))
}

// cwtScalarString renders a scalar the way a string-typed consumer reads it.
func cwtScalarString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case json.Number:
		return t.String(), true
	case bool:
		if t {
			return "true", true
		}
		return "false", true
	}
	return "", false
}
