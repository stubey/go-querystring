// Copyright 2013 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package query implements encoding of structs into URL query parameters.
//
// As a simple example:
//
// 	type Options struct {
// 		Query   string `url:"q"`
// 		ShowAll bool   `url:"all"`
// 		Page    int    `url:"page"`
// 	}
//
// 	opt := Options{ "foo", true, 2 }
// 	v, _ := query.Values(opt)
// 	fmt.Print(v.Encode()) // will output: "q=foo&all=true&page=2"
//
// The exact mapping between Go values and url.Values is described in the
// documentation for the Values() function.
package query

import (
	"bytes"
	"fmt"
	"log"
	"net/url"
	"path"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var timeType = reflect.TypeOf(time.Time{})

var encoderType = reflect.TypeOf(new(Encoder)).Elem()

// Encoder is an interface implemented by any type that wishes to encode
// itself into URL values in a non-standard way.
type Encoder interface {
	EncodeValues(key string, v *url.Values) error
}

// Values returns the url.Values encoding of v.
//
// Values expects to be passed a struct, and traverses it recursively using the
// following encoding rules.
//
// Each exported struct field is encoded as a URL parameter unless
//
//	- the field's tag is "-", or
//	- the field is empty and its tag specifies the "omitempty" option
//
// The empty values are false, 0, any nil pointer or interface value, any array
// slice, map, or string of length zero, and any time.Time that returns true
// for IsZero().
//
// The URL parameter name defaults to the struct field name but can be
// specified in the struct field's tag value.  The "url" key in the struct
// field's tag value is the key name, followed by an optional comma and
// options.  For example:
//
// 	// Field is ignored by this package.
// 	Field int `url:"-"`
//
// 	// Field appears as URL parameter "myName".
// 	Field int `url:"myName"`
//
// 	// Field appears as URL parameter "myName" and the field is omitted if
// 	// its value is empty
// 	Field int `url:"myName,omitempty"`
//
// 	// Field appears as URL parameter "Field" (the default), but the field
// 	// is skipped if empty.  Note the leading comma.
// 	Field int `url:",omitempty"`
//
// For encoding individual field values, the following type-dependent rules
// apply:
//
// Boolean values default to encoding as the strings "true" or "false".
// Including the "int" option signals that the field should be encoded as the
// strings "1" or "0".
//
// time.Time values default to encoding as RFC3339 timestamps.  Including the
// "unix" option signals that the field should be encoded as a Unix time (see
// time.Unix())
//
// Slice and Array values default to encoding as multiple URL values of the
// same name.  Including the "comma" option signals that the field should be
// encoded as a single comma-delimited value.  Including the "space" option
// similarly encodes the value as a single space-delimited string. Including
// the "semicolon" option will encode the value as a semicolon-delimited string.
// Including the "brackets" option signals that the multiple URL values should
// have "[]" appended to the value name. "numbered" will append a number to
// the end of each incidence of the value name, example:
// name0=value0&name1=value1, etc.
//
// Anonymous struct fields are usually encoded as if their inner exported
// fields were fields in the outer struct, subject to the standard Go
// visibility rules.  An anonymous struct field with a name given in its URL
// tag is treated as having that name, rather than being anonymous.
//
// Non-nil pointer values are encoded as the value pointed to.
//
// Nested structs are encoded including parent fields in value names for
// scoping. e.g:
//
// 	"user[name]=acme&user[addr][postcode]=1234&user[addr][city]=SFO"
//
// All other values are encoded using their default string representation.
//
// Multiple fields that encode to the same URL parameter name will be included
// as multiple URL values of the same name.

// v is generally a struct or pointer-to-struct
// Return empty values if nil-pointer or a nil value
// Return error if v is neither struct nor ptr-to-struct
func Values(v interface{}) (url.Values, error) {
	logit("\n\nv", v)

	// url.Values is a map[string] []string
	values := make(url.Values)

	// Set val to the interfaces Value
	val := reflect.ValueOf(v)
	logit("val", val)

	// Update val to remove 'Pointieness' (dereference the pointer)
	for val.Kind() == reflect.Ptr {
		// Return if nil pointer
		if val.IsNil() {
			logit("val is a nil pointer = ", true)
			return values, nil
		}
		// Dereference the pointer
		val = val.Elem()
	}

	// Return if nil value
	if v == nil {
		logit("val is a nil value = ", true)
		return values, nil
	}

	logit("val", val)
	// Return if non-struct value
	if val.Kind() != reflect.Struct {
		logit("val is not a struct = ", true)
		return nil, fmt.Errorf("query: Values() expects struct input. Got %v", val.Kind())
	}

	// Populate values with tag name and values
	// maps (values) are modifiable by the called function
	err := reflectValue(values, val, "")
	logit("values", values)
	logit("--------", "--------")
	return values, err
}

// reflectValue populates the values parameter from the struct fields in val.
// Embedded structs are followed recursively (using the rules defined in the
// Values function documentation) breadth-first.
// Caller should have filtered out non-structs
func reflectValue(values url.Values, val reflect.Value, scope string) error {
	logit("\n\nval", val)
	logit("\n\nscope", scope)

	var embedded []reflect.Value

	typ := val.Type()
	logit("typ", typ)

	for i := 0; i < typ.NumField(); i++ {
		logit("\n\n**** Field #", i)

		sf := typ.Field(i)
		logit("sf", sf)
		logit("sf.PkgPath", sf.PkgPath)
		logit("sf.Anonymous", sf.Anonymous)

		// Ignore field if field is unexported
		// sf.PkgPath != "" if lowercase field name
		// sf.Anonymous == embedded field
		if sf.PkgPath != "" && !sf.Anonymous { // unexported
			logit("unexported - continue", true)
			continue
		}

		sv := val.Field(i)
		logit("sv", sv)

		tag := sf.Tag.Get("url")
		logit("url tag", tag)

		// Ignore field if tag name == "-"
		if tag == "-" {
			logit("tag is unexported due to - - continue", true)
			continue
		}
		name, opts := parseTag(tag)
		logit("name", name)
		logit("opts", opts)

		// If no name specified, use the Field name
		if name == "" {
			logit("name == ''", true)

			logit("sv.Kind()", sv.Kind())

			// Defer embedded struct processing (save and continue)
			if sf.Anonymous && sv.Kind() == reflect.Struct {
				// save embedded struct for later processing
				logit("Embedded (Anonymous) struct - save sv for later and continue", true)
				embedded = append(embedded, sv)
				continue
			}

			name = sf.Name
			logit("Set name to field name", name)
		}

		if scope != "" {
			name = scope + "[" + name + "]"
			logit("updated, scoped name", name)
		}

		if opts.Contains("omitempty") && isEmptyValue(sv) {
			logit("omitempty option - continue", true)
			continue
		}

		// Detect if sv.Type() implements Encoder
		if sv.Type().Implements(encoderType) {
			logit("custom encoder", true)
			//  Detect if nil Encoder interface ptr
			if !reflect.Indirect(sv).IsValid() {
				// Instantiate a zero value Encoder if ptr is nil
				logit("sv NotValid", true)
				logit("sv.Type().Kind()", sv.Type().Kind())
				logit("sv.Type().Elem()", sv.Type().Elem())
				sv = reflect.New(sv.Type().Elem())
			}

			m := sv.Interface().(Encoder)
			if err := m.EncodeValues(name, &values); err != nil {
				return err
			}
			logit("use custom encoder - continue", true)
			continue
		}

		if sv.Kind() == reflect.Slice || sv.Kind() == reflect.Array {
			var del byte
			if opts.Contains("comma") {
				del = ','
			} else if opts.Contains("space") {
				del = ' '
			} else if opts.Contains("semicolon") {
				del = ';'
			} else if opts.Contains("brackets") {
				name = name + "[]"
			}

			if del != 0 {
				s := new(bytes.Buffer)
				first := true
				for i := 0; i < sv.Len(); i++ {
					if first {
						first = false
					} else {
						s.WriteByte(del)
					}
					s.WriteString(valueString(sv.Index(i), opts))
				}
				values.Add(name, s.String())
			} else {
				for i := 0; i < sv.Len(); i++ {
					k := name
					if opts.Contains("numbered") {
						k = fmt.Sprintf("%s%d", name, i)
					}
					values.Add(k, valueString(sv.Index(i), opts))
				}
			}
			continue
		}

		if sv.Type() == timeType {
			values.Add(name, valueString(sv, opts))
			continue
		}

		for sv.Kind() == reflect.Ptr {
			if sv.IsNil() {
				break
			}
			sv = sv.Elem()
		}

		if sv.Kind() == reflect.Struct {
			reflectValue(values, sv, name)
			continue
		}

		values.Add(name, valueString(sv, opts))
	}

	for _, f := range embedded {
		if err := reflectValue(values, f, scope); err != nil {
			return err
		}
	}

	return nil
}

// valueString returns the string representation of a value.
func valueString(v reflect.Value, opts tagOptions) string {
	for v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return ""
		}
		v = v.Elem()
	}

	if v.Kind() == reflect.Bool && opts.Contains("int") {
		if v.Bool() {
			return "1"
		}
		return "0"
	}

	if v.Type() == timeType {
		t := v.Interface().(time.Time)
		if opts.Contains("unix") {
			return strconv.FormatInt(t.Unix(), 10)
		}
		return t.Format(time.RFC3339)
	}

	return fmt.Sprint(v.Interface())
}

// isEmptyValue checks if a value should be considered empty for the purposes
// of omitting fields with the "omitempty" option.
func isEmptyValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool:
		return !v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	case reflect.Interface, reflect.Ptr:
		return v.IsNil()
	}

	if v.Type() == timeType {
		return v.Interface().(time.Time).IsZero()
	}

	return false
}

// tagOptions is the string following a comma in a struct field's "url" tag, or
// the empty string. It does not include the leading comma.
type tagOptions []string

// parseTag splits a struct field's url tag into its name and comma-separated
// options.
func parseTag(tag string) (string, tagOptions) {
	s := strings.Split(tag, ",")
	return s[0], s[1:]
}

// Contains checks whether the tagOptions contains the specified option.
func (o tagOptions) Contains(option string) bool {
	for _, s := range o {
		if s == option {
			return true
		}
	}
	return false
}

func logit(m string, val interface{}) {
	//pc, fn, line, _ := runtime.Caller(1)
	//log.Printf("%s[%s:%d] %v (type %T_ = %+v", runtime.FuncForPC(pc).Name(), fn, line, m, val, val)
	log.SetFlags(0)
	_, fn, line, _ := runtime.Caller(1)
	fn = path.Base(fn)
	log.Printf("%v - L%d %v (type %T) = %+v", fn, line, m, val, val)
}
