// Copyright (c) 2017 Uber Technologies, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package mapdecode

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/uber-go/mapdecode/internal/mapstructure"

	"go.uber.org/multierr"
)

var (
	_typeOfDuration       = reflect.TypeOf(time.Duration(0))
	_typeOfEmptyInterface = reflect.TypeOf((*interface{})(nil)).Elem()
)

// FieldHookFunc is a hook called while decoding a specific struct field. It
// receives the source type, information about the target field, and the
// source data.
type FieldHookFunc func(from reflect.Type, to reflect.StructField, data reflect.Value) (reflect.Value, error)

func composeFieldHooks(hooks []FieldHookFunc) FieldHookFunc {
	return func(from reflect.Type, to reflect.StructField, data reflect.Value) (reflect.Value, error) {
		var err error
		for _, hook := range hooks {
			data, err = hook(from, to, data)
			if err != nil {
				return data, err
			}

			// Update the `from` type to reflect changes made by the hook.
			from = data.Type()
		}
		return data, err
	}
}

// DecodeHookFunc is a hook called before decoding a value into a specific
// type.
type DecodeHookFunc func(from, to reflect.Type, data reflect.Value) (reflect.Value, error)

// Note: DecodeHookFunc is a reflect.Value version of DecodeHookFuncType.

// Builds a mapstructure-compatible hook from a DecodeHookFunc.
func fromDecodeHookFunc(hook DecodeHookFunc) mapstructure.DecodeHookFuncType {
	return func(from, to reflect.Type, data interface{}) (interface{}, error) {
		var value reflect.Value
		if data != nil {
			value = reflect.ValueOf(data)
		} else {
			// mapstructure is pretty good about giving us non-nil data but
			// let's process it gracefully anyway.
			value = reflect.Zero(from)
		}

		out, err := hook(from, to, value)
		if err != nil {
			return nil, err
		}

		return out.Interface(), nil
	}
}

// Composes multiple DecodeHookFuncs into one. The hooks are applied in-order
// and values produced by a hook are fed into the next hook.
func composeDecodeHooks(hooks []DecodeHookFunc) DecodeHookFunc {
	return func(from, to reflect.Type, data reflect.Value) (reflect.Value, error) {
		var err error
		for _, hook := range hooks {
			data, err = hook(from, to, data)
			if err != nil {
				return data, err
			}

			// Update the `from` type to reflect changes made by the hook.
			from = data.Type()
		}
		return data, err
	}
}

// Wraps a DecodeHookFunc to support pointers in either direction (source and
// destination).
func supportPointers(hook DecodeHookFunc) (outputHook DecodeHookFunc) {
	outputHook = func(from, to reflect.Type, data reflect.Value) (reflect.Value, error) {
		// Get rid of pointers in either direction. This lets us parse **foo if we
		// know how to parse foo.
		switch {
		case from.Kind() == reflect.Ptr: // *foo => bar
			// Decoding a pointer type to a non-pointer type. Dereference if
			// non-nil, use zero-value otherwise.
			from = from.Elem()
			if data.IsNil() {
				data = reflect.Zero(from)
			} else {
				data = data.Elem()
			}
			return outputHook(from, to, data)
		case to.Kind() == reflect.Ptr: // foo => *bar
			// Decoding a non-pointer type to a pointer. Decode as usual and take
			// a pointer to the result.
			out, err := outputHook(from, to.Elem(), data)
			if err != nil {
				return out, err
			}

			// If we didn't know what to do with the input, the returned value
			// will just be the data as-is and it won't have the correct type.
			if !out.Type().AssignableTo(to.Elem()) {
				return data, nil
			}

			result := reflect.New(to.Elem())
			result.Elem().Set(out)
			return result, nil
		}

		return hook(from, to, data)
	}
	return
}

// Builds a DecodeHookFunc which unmarshals types using the given unmarshaling
// scheme. See the unmarshaler type for more information.
func unmarshalerHook(opts *options) DecodeHookFunc {
	return func(from, to reflect.Type, data reflect.Value) (reflect.Value, error) {
		if from == to {
			return data, nil
		}

		if !reflect.PtrTo(to).Implements(opts.Unmarshaler.Interface) {
			return data, nil
		}

		// The following lines are roughly equivalent to,
		// 	value := new(foo)
		// 	err := value.Decode(...)
		// 	return *value, err
		value := reflect.New(to)
		err := opts.Unmarshaler.Unmarshal(value, decodeFrom(opts, data.Interface()))
		if err != nil {
			err = fmt.Errorf("could not decode %v from %v: %v", to, from, err)
		}
		return value.Elem(), err
	}
}

// A DecodeHookFunc which decodes strings into time.Durations.
func durationHook(from, to reflect.Type, data reflect.Value) (reflect.Value, error) {
	if from.Kind() != reflect.String || to != _typeOfDuration {
		return data, nil
	}

	d, err := time.ParseDuration(data.String())
	return reflect.ValueOf(d), err
}

// stringToPrimitivesHook is a DecodeHookFunc which decodes strings into
// primitives.
//
// Integers are parsed in base 10.
func strconvHook(from, to reflect.Type, data reflect.Value) (reflect.Value, error) {
	if from.Kind() != reflect.String {
		return data, nil
	}

	s := data.String()
	switch to.Kind() {
	case reflect.Bool:
		b, err := strconv.ParseBool(s)
		return reflect.ValueOf(b), err
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(s, to.Bits())
		return reflect.ValueOf(f).Convert(to), err
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		i, err := strconv.ParseInt(s, 10, to.Bits())
		return reflect.ValueOf(i).Convert(to), err
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		u, err := strconv.ParseUint(s, 10, to.Bits())
		return reflect.ValueOf(u).Convert(to), err
	}

	return data, nil
}

// fieldHook applies the user-specified FieldHookFunc to all struct fields.
func fieldHook(opts *options) DecodeHookFunc {
	hook := composeFieldHooks(opts.FieldHooks)
	return func(_, destType reflect.Type, srcData reflect.Value) (reflect.Value, error) {
		if destType.Kind() != reflect.Struct || srcData.Type().Kind() != reflect.Map {
			return srcData, nil
		}

		// We can only decode map[string]* and map[interface{}]* into structs.
		if k := srcData.Type().Key().Kind(); k != reflect.String && k != reflect.Interface {
			return srcData, nil
		}

		// This map tracks type-changing updates to items in the map.
		//
		// If the source map has a rigid type for values (map[string]string
		// rather than map[string]interface{}), we can't make replacements to
		// values in-place if a hook changed the type of a value. So we will
		// make a copy of the source map with a more liberal type and inject
		// these updates into the copy.
		updates, err := getMapUpdates(destType, srcData, hook, opts.TagName)
		if err != nil {
			return srcData, err
		}

		// No more changes to make.
		if len(updates) == 0 {
			return srcData, nil
		}

		return applyUpdates(updates, srcData), nil
	}
}

func getMapUpdates(destType reflect.Type, srcData reflect.Value, hook FieldHookFunc, tagName string) (map[interface{}]interface{}, error) {
	updates := make(map[interface{}]interface{})
	var errors []error

	decodableFields := getDecodableStructFields(destType)
	for _, structField := range decodableFields {
		// This field resolution logic is adapted from mapstructure's own
		// logic.
		//
		// See https://github.com/mitchellh/mapstructure/blob/53818660ed4955e899c0bcafa97299a388bd7c8e/mapstructure.go#L741

		fieldName := structField.Name

		// Field name override was specified.
		tagParts := strings.Split(structField.Tag.Get(tagName), ",")
		if tagParts[0] != "" {
			fieldName = tagParts[0]
		}

		// Get the value for this field from the source map, if any.
		key := reflect.ValueOf(fieldName)
		value := srcData.MapIndex(key)
		if !value.IsValid() {
			// Case-insensitive linear search if the name doesn't match
			// as-is.
			for _, kV := range srcData.MapKeys() {
				// Kind() == Interface if map[interface{}]* so we use
				// Interface().(string) to handle interface{} and string
				// keys.
				k, ok := kV.Interface().(string)
				if !ok {
					continue
				}

				if strings.EqualFold(k, fieldName) {
					key = kV
					value = srcData.MapIndex(kV)
					break
				}
			}
		}

		if !value.IsValid() {
			// No value specified for this field in source map.
			continue
		}

		newValue, err := hook(value.Type(), structField, value)
		if err != nil {
			errors = append(errors, fmt.Errorf(
				"error reading into field %q: %v", fieldName, err))
			continue
		}

		if newValue == value {
			continue
		}

		updates[key.Interface()] = newValue.Interface()
	}

	if len(errors) > 0 {
		return nil, multierr.Combine(errors...)
	}

	return updates, nil
}

func getDecodableStructFields(structType reflect.Type) []reflect.StructField {
	fields := make([]reflect.StructField, 0)
	for i := 0; i < structType.NumField(); i++ {
		structField := structType.Field(i)
		if structField.PkgPath != "" && !structField.Anonymous {
			// This field is not exported so we won't be able to decode
			// into it.
			continue
		}
		// TODO account for embedded struct fields
		fields = append(fields, structField)
	}
	return fields
}

func applyUpdates(updates map[interface{}]interface{}, srcData reflect.Value) reflect.Value {
	// Equivalent to,
	//
	// 	newData := make(map[$key]interface{})
	// 	for k, v := range data {
	// 		if newV, ok := updates[k]; ok {
	// 			newData[k] = newV
	// 		} else {
	// 			newData[k] = v
	// 		}
	// 	}
	newData := reflect.MakeMap(reflect.MapOf(srcData.Type().Key(), _typeOfEmptyInterface))
	for _, key := range srcData.MapKeys() {
		var value reflect.Value
		if v, ok := updates[key.Interface()]; ok {
			value = reflect.ValueOf(v)
		} else {
			value = srcData.MapIndex(key)
		}
		newData.SetMapIndex(key, value)
	}
	return newData
}
