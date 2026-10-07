package config

import "reflect"

// cloneConfigValue detaches maps, slices, interfaces, and pointers from the
// values held by Viper before returning them to callers.
func cloneConfigValue(value any) any {
	if value == nil {
		return nil
	}

	cloned := cloneReflectValue(reflect.ValueOf(value))
	if !cloned.IsValid() {
		return nil
	}
	return cloned.Interface()
}

func cloneReflectValue(value reflect.Value) reflect.Value {
	if !value.IsValid() {
		return value
	}

	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		inner := cloneReflectValue(value.Elem())
		out := reflect.New(value.Type()).Elem()
		if inner.IsValid() && inner.Type().AssignableTo(value.Type()) {
			out.Set(inner)
		}
		return out
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		out := reflect.MakeMapWithSize(value.Type(), value.Len())
		for _, key := range value.MapKeys() {
			item := cloneReflectValue(value.MapIndex(key))
			if !item.IsValid() || !item.Type().AssignableTo(value.Type().Elem()) {
				item = value.MapIndex(key)
			}
			out.SetMapIndex(key, item)
		}
		return out
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		out := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for i := 0; i < value.Len(); i++ {
			item := cloneReflectValue(value.Index(i))
			if item.IsValid() && item.Type().AssignableTo(value.Type().Elem()) {
				out.Index(i).Set(item)
			} else {
				out.Index(i).Set(value.Index(i))
			}
		}
		return out
	case reflect.Ptr:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		out := reflect.New(value.Type().Elem())
		item := cloneReflectValue(value.Elem())
		if item.IsValid() && item.Type().AssignableTo(value.Type().Elem()) {
			out.Elem().Set(item)
		} else {
			out.Elem().Set(value.Elem())
		}
		return out
	default:
		return value
	}
}
