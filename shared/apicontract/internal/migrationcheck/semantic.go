package migrationcheck

import "reflect"

type SemanticKind string

const (
	SemanticBool    SemanticKind = "bool"
	SemanticInt8    SemanticKind = "int8"
	SemanticInt16   SemanticKind = "int16"
	SemanticInt32   SemanticKind = "int32"
	SemanticInt64   SemanticKind = "int64"
	SemanticUint8   SemanticKind = "uint8"
	SemanticUint16  SemanticKind = "uint16"
	SemanticUint32  SemanticKind = "uint32"
	SemanticUint64  SemanticKind = "uint64"
	SemanticFloat32 SemanticKind = "float32"
	SemanticFloat64 SemanticKind = "float64"
	SemanticString  SemanticKind = "string"
	SemanticBytes   SemanticKind = "bytes"
	SemanticMessage SemanticKind = "message"
)

func semanticKindOf(legacyType reflect.Type) SemanticKind {
	switch legacyType.Kind() {
	case reflect.Bool:
		return SemanticBool
	case reflect.Int8:
		return SemanticInt8
	case reflect.Int16:
		return SemanticInt16
	case reflect.Int32:
		return SemanticInt32
	case reflect.Int, reflect.Int64:
		return SemanticInt64
	case reflect.Uint8:
		return SemanticUint8
	case reflect.Uint16:
		return SemanticUint16
	case reflect.Uint32:
		return SemanticUint32
	case reflect.Uint, reflect.Uint64:
		return SemanticUint64
	case reflect.Float32:
		return SemanticFloat32
	case reflect.Float64:
		return SemanticFloat64
	case reflect.String:
		return SemanticString
	case reflect.Slice:
		if legacyType.Elem().Kind() == reflect.Uint8 {
			return SemanticBytes
		}
	case reflect.Struct:
		return SemanticMessage
	}
	return ""
}

func legacySemanticTypePath(legacyType reflect.Type) string {
	legacyType = dereferenceType(legacyType)
	if legacyType.PkgPath() == "" {
		return legacyType.String()
	}
	return legacyType.PkgPath() + "." + legacyType.Name()
}

func dereferenceType(value reflect.Type) reflect.Type {
	for value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	return value
}
