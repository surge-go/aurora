package logger

import (
	"fmt"
	"time"

	"go.uber.org/zap"
)

// Field 是 logger 对外暴露的结构化字段类型。
//
// 业务代码不需要依赖 zap.Field；具体的编码实现只在 logger 包内部完成。
type Field struct {
	key   string
	kind  fieldKind
	value any
	skip  bool
}

type fieldKind uint8

const (
	fieldString fieldKind = iota
	fieldBool
	fieldInt
	fieldInt8
	fieldInt16
	fieldInt32
	fieldInt64
	fieldUint
	fieldUint8
	fieldUint16
	fieldUint32
	fieldUint64
	fieldFloat32
	fieldFloat64
	fieldDuration
	fieldTime
	fieldError
	fieldAny
	fieldStrings
	fieldBinary
	fieldByteString
	fieldStringer
	fieldNamespace
)

// String 创建字符串字段。
func String(key, value string) Field {
	return Field{key: key, kind: fieldString, value: value}
}

// Bool 创建布尔字段。
func Bool(key string, value bool) Field {
	return Field{key: key, kind: fieldBool, value: value}
}

// Int 创建 int 字段。
func Int(key string, value int) Field {
	return Field{key: key, kind: fieldInt, value: value}
}

// Int8 创建 int8 字段。
func Int8(key string, value int8) Field {
	return Field{key: key, kind: fieldInt8, value: value}
}

// Int16 创建 int16 字段。
func Int16(key string, value int16) Field {
	return Field{key: key, kind: fieldInt16, value: value}
}

// Int32 创建 int32 字段。
func Int32(key string, value int32) Field {
	return Field{key: key, kind: fieldInt32, value: value}
}

// Int64 创建 int64 字段。
func Int64(key string, value int64) Field {
	return Field{key: key, kind: fieldInt64, value: value}
}

// Uint 创建 uint 字段。
func Uint(key string, value uint) Field {
	return Field{key: key, kind: fieldUint, value: value}
}

// Uint8 创建 uint8 字段。
func Uint8(key string, value uint8) Field {
	return Field{key: key, kind: fieldUint8, value: value}
}

// Uint16 创建 uint16 字段。
func Uint16(key string, value uint16) Field {
	return Field{key: key, kind: fieldUint16, value: value}
}

// Uint32 创建 uint32 字段。
func Uint32(key string, value uint32) Field {
	return Field{key: key, kind: fieldUint32, value: value}
}

// Uint64 创建 uint64 字段。
func Uint64(key string, value uint64) Field {
	return Field{key: key, kind: fieldUint64, value: value}
}

// Float32 创建 float32 字段。
func Float32(key string, value float32) Field {
	return Field{key: key, kind: fieldFloat32, value: value}
}

// Float64 创建 float64 字段。
func Float64(key string, value float64) Field {
	return Field{key: key, kind: fieldFloat64, value: value}
}

// Duration 创建 time.Duration 字段。
func Duration(key string, value time.Duration) Field {
	return Field{key: key, kind: fieldDuration, value: value}
}

// Time 创建 time.Time 字段。
func Time(key string, value time.Time) Field {
	return Field{key: key, kind: fieldTime, value: value}
}

// Error 创建 error 字段。nil error 会被忽略。
func Error(err error) Field {
	if err == nil {
		return Field{skip: true}
	}
	return Field{key: "error", kind: fieldError, value: err}
}

// NamedError 创建自定义名称的 error 字段。nil error 会被忽略。
func NamedError(key string, err error) Field {
	if err == nil {
		return Field{skip: true}
	}
	return Field{key: key, kind: fieldError, value: err}
}

// Any 创建任意值字段。
func Any(key string, value any) Field {
	return Field{key: key, kind: fieldAny, value: value}
}

// Strings 创建字符串数组字段。
func Strings(key string, value []string) Field {
	return Field{key: key, kind: fieldStrings, value: value}
}

// Binary 创建二进制字段。
func Binary(key string, value []byte) Field {
	return Field{key: key, kind: fieldBinary, value: value}
}

// ByteString 创建 UTF-8 字节字符串字段。
func ByteString(key string, value []byte) Field {
	return Field{key: key, kind: fieldByteString, value: value}
}

// Stringer 创建 fmt.Stringer 字段。
func Stringer(key string, value fmt.Stringer) Field {
	return Field{key: key, kind: fieldStringer, value: value}
}

// Namespace 创建字段命名空间。
func Namespace(key string) Field {
	return Field{key: key, kind: fieldNamespace}
}

func (f Field) isEmpty() bool {
	return f.skip || (f.key == "" && f.value == nil && f.kind == fieldString)
}

func (f Field) toZap() zap.Field {
	switch f.kind {
	case fieldString:
		return zap.String(f.key, f.value.(string))
	case fieldBool:
		return zap.Bool(f.key, f.value.(bool))
	case fieldInt:
		return zap.Int(f.key, f.value.(int))
	case fieldInt8:
		return zap.Int8(f.key, f.value.(int8))
	case fieldInt16:
		return zap.Int16(f.key, f.value.(int16))
	case fieldInt32:
		return zap.Int32(f.key, f.value.(int32))
	case fieldInt64:
		return zap.Int64(f.key, f.value.(int64))
	case fieldUint:
		return zap.Uint(f.key, f.value.(uint))
	case fieldUint8:
		return zap.Uint8(f.key, f.value.(uint8))
	case fieldUint16:
		return zap.Uint16(f.key, f.value.(uint16))
	case fieldUint32:
		return zap.Uint32(f.key, f.value.(uint32))
	case fieldUint64:
		return zap.Uint64(f.key, f.value.(uint64))
	case fieldFloat32:
		return zap.Float32(f.key, f.value.(float32))
	case fieldFloat64:
		return zap.Float64(f.key, f.value.(float64))
	case fieldDuration:
		return zap.Duration(f.key, f.value.(time.Duration))
	case fieldTime:
		return zap.Time(f.key, f.value.(time.Time))
	case fieldError:
		return zap.NamedError(f.key, f.value.(error))
	case fieldAny:
		return zap.Any(f.key, f.value)
	case fieldStrings:
		return zap.Strings(f.key, f.value.([]string))
	case fieldBinary:
		return zap.Binary(f.key, f.value.([]byte))
	case fieldByteString:
		return zap.ByteString(f.key, f.value.([]byte))
	case fieldStringer:
		return zap.Stringer(f.key, f.value.(fmt.Stringer))
	case fieldNamespace:
		return zap.Namespace(f.key)
	default:
		return zap.Skip()
	}
}

func toZapFields(fields []Field) []zap.Field {
	if len(fields) == 0 {
		return nil
	}

	zapFields := make([]zap.Field, 0, len(fields))
	for _, field := range fields {
		if field.isEmpty() {
			continue
		}
		zapFields = append(zapFields, field.toZap())
	}
	return zapFields
}
