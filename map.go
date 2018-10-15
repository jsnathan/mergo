// Copyright 2014 Dario Castañé. All rights reserved.
// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Based on src/pkg/reflect/deepequal.go from official
// golang's stdlib.

package mergo

import (
	"encoding/json"
	"fmt"
	"reflect"
	"unicode"
	"unicode/utf8"
)

func changeInitialCase(s string, mapper func(rune) rune) string {
	if s == "" {
		return s
	}
	r, n := utf8.DecodeRuneInString(s)
	return string(mapper(r)) + s[n:]
}

func isExported(field reflect.StructField) bool {
	r, _ := utf8.DecodeRuneInString(field.Name)
	return r >= 'A' && r <= 'Z'
}

// Traverses recursively both values, assigning src's fields values to dst.
// The map argument tracks comparisons that have already been seen, which allows
// short circuiting on recursive types.
func deepMap(dst, src reflect.Value, visited map[uintptr]*visit, depth int, config *Config) (err error) {
	overwrite := config.Overwrite
	if dst.CanAddr() {
		addr := dst.UnsafeAddr()
		h := 17 * addr
		seen := visited[h]
		typ := dst.Type()
		for p := seen; p != nil; p = p.next {
			if p.ptr == addr && p.typ == typ {
				return nil
			}
		}
		// Remember, remember...
		visited[h] = &visit{addr, typ, seen}
	}
	zeroValue := reflect.Value{}
	switch dst.Kind() {
	case reflect.Map:
		dstMap := dst.Interface().(map[string]interface{})
		for i, n := 0, src.NumField(); i < n; i++ {
			srcType := src.Type()
			field := srcType.Field(i)
			if !isExported(field) {
				continue
			}
			fieldName := field.Name
			fieldName = changeInitialCase(fieldName, unicode.ToLower)
			if v, ok := dstMap[fieldName]; !ok || (isEmptyValue(reflect.ValueOf(v)) || overwrite) {
				dstMap[fieldName] = src.Field(i).Interface()
			}
		}
	case reflect.Ptr:
		if dst.IsNil() {
			v := reflect.New(dst.Type().Elem())
			dst.Set(v)
		}
		dst = dst.Elem()
		fallthrough
	case reflect.Struct:
		srcMap := src.Interface().(map[string]interface{})
		for key := range srcMap {
			srcValue := srcMap[key]
			fieldName := changeInitialCase(key, unicode.ToUpper)
			dstElement := dst.FieldByName(fieldName)
			srcElement := reflect.ValueOf(srcValue)
      if srcElement == zeroValue {
				// We ignore it because otherwise things break.
        continue
      }
			if srcElement.Type() == reflect.TypeOf(json.Number("")) {
				err = jsonNumberTransformer(dstElement, srcElement)
				if err != nil {
					return
				}
				continue
			}
			if dstElement == zeroValue {
				// We discard it because the field doesn't exist.
				continue
			}
			dstKind := dstElement.Kind()
			srcKind := srcElement.Kind()
			if srcKind == reflect.Ptr && dstKind != reflect.Ptr {
				srcElement = srcElement.Elem()
				srcKind = reflect.TypeOf(srcElement.Interface()).Kind()
			} else if dstKind == reflect.Ptr {
				// Can this work? I guess it can't.
				if srcKind != reflect.Ptr && srcElement.CanAddr() {
					srcPtr := srcElement.Addr()
					srcElement = reflect.ValueOf(srcPtr)
					srcKind = reflect.Ptr
				}
			}

			if !srcElement.IsValid() {
				continue
			}
			if srcKind == dstKind {
				if err = deepMerge(dstElement, srcElement, visited, depth+1, config); err != nil {
					return
				}
			} else if dstKind == reflect.Interface && dstElement.Kind() == reflect.Interface {
				if err = deepMerge(dstElement, srcElement, visited, depth+1, config); err != nil {
					return
				}
			} else if srcKind == reflect.Map {
				if err = deepMap(dstElement, srcElement, visited, depth+1, config); err != nil {
					return
				}
			} else if config.NumberConversion && convertNumber(dstElement, srcElement) {
        continue
      } else {
				return fmt.Errorf("type mismatch on %s field: found %v, expected %v", fieldName, srcKind, dstKind)
			}
		}
	}
	return
}

// Map sets fields' values in dst from src.
// src can be a map with string keys or a struct. dst must be the opposite:
// if src is a map, dst must be a valid pointer to struct. If src is a struct,
// dst must be map[string]interface{}.
// It won't merge unexported (private) fields and will do recursively
// any exported field.
// If dst is a map, keys will be src fields' names in lower camel case.
// Missing key in src that doesn't match a field in dst will be skipped. This
// doesn't apply if dst is a map.
// This is separated method from Merge because it is cleaner and it keeps sane
// semantics: merging equal types, mapping different (restricted) types.
func Map(dst, src interface{}, opts ...func(*Config)) error {
	return _map(dst, src, opts...)
}

// MapWithOverwrite will do the same as Map except that non-empty dst attributes will be overridden by
// non-empty src attribute values.
// Deprecated: Use Map(…) with WithOverride
func MapWithOverwrite(dst, src interface{}, opts ...func(*Config)) error {
	return _map(dst, src, append(opts, WithOverride)...)
}

func _map(dst, src interface{}, opts ...func(*Config)) error {
	var (
		vDst, vSrc reflect.Value
		err        error
	)
	config := &Config{}

	for _, opt := range opts {
		opt(config)
	}

	if vDst, vSrc, err = resolveValues(dst, src); err != nil {
		return err
	}
	// To be friction-less, we redirect equal-type arguments
	// to deepMerge. Only because arguments can be anything.
	if vSrc.Kind() == vDst.Kind() {
		return deepMerge(vDst, vSrc, make(map[uintptr]*visit), 0, config)
	}
	switch vSrc.Kind() {
	case reflect.Struct:
		if vDst.Kind() != reflect.Map {
			return ErrExpectedMapAsDestination
		}
	case reflect.Map:
		if vDst.Kind() != reflect.Struct {
			return ErrExpectedStructAsDestination
		}
	default:
		return ErrNotSupported
	}
	return deepMap(vDst, vSrc, make(map[uintptr]*visit), 0, config)
}

func jsonNumberTransformer(dst, src reflect.Value) error {
	if dst.CanSet() != true {
		return fmt.Errorf("number field not settable")
	}
	num := src.Interface().(json.Number)
	switch dst.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		numAsInt64, err := num.Int64()
		if err != nil {
			return err
		}
		dst.SetInt(numAsInt64)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		numAsInt64, err := num.Int64()
		if err != nil {
			return err
		}
		if numAsInt64 < 0 {
			return fmt.Errorf("cannot assign negative json.Number to unsigned integer field")
		}
		dst.SetUint(uint64(numAsInt64))

	case reflect.Float32, reflect.Float64:
		numAsFloat64, err := num.Float64()
		if err != nil {
			return err
		}
		dst.SetFloat(numAsFloat64)
	default:
		return fmt.Errorf("cannot assign json.Number to non-number field")
	}
	return nil
}

func convertNumber(dst, src reflect.Value) bool {
	switch src.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
    return storeNumber(dst, src.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
    return storeNumber(dst, src.Uint())
	case reflect.Float32, reflect.Float64:
    return storeNumber(dst, src.Float())
	default:
    fmt.Printf("unknown number src format: %v\n", src.Kind())
		return false
	}
}

func storeNumber(dst reflect.Value, num interface{}) bool {
	if dst.CanSet() != true {
    fmt.Printf("cannot set dst to %v (field not writeable?)\n", num)
		return false
	}
	switch dst.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
    switch n := num.(type) {
      case uint64: dst.SetInt(int64(n))
      case float64: dst.SetInt(int64(n))
      case int64: dst.SetInt(n)
      default: panic(fmt.Sprintf("Unknown number type passed: %T", num))
    }
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
    switch n := num.(type) {
      case uint64: dst.SetUint(n)
      case float64: dst.SetUint(uint64(n))
      case int64: dst.SetUint(uint64(n))
      default: panic(fmt.Sprintf("Unknown number type passed: %T", num))
    }
	case reflect.Float32, reflect.Float64:
    switch n := num.(type) {
      case uint64: dst.SetFloat(float64(n))
      case float64: dst.SetFloat(n)
      case int64: dst.SetFloat(float64(n))
      default: panic(fmt.Sprintf("Unknown number type passed: %T", num))
    }
	default:
    fmt.Printf("cannot set number to non-number field dst of kind %v\n", dst.Kind())
		return false
	}
  return true
}
