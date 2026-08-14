package migrationcheck

import "reflect"

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
