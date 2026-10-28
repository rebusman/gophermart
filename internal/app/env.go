package app

import "os"

// envReader читает переменные окружения через подменяемую функцию поиска.
type envReader struct {
	lookup func(string) (string, bool)
}

// newEnvReader создаёт читателя окружения. Если lookup равен nil, используется
func newEnvReader(lookup func(string) (string, bool)) envReader {
	if lookup == nil {
		lookup = os.LookupEnv
	}

	return envReader{lookup: lookup}
}

// Lookup возвращает значение переменной окружения и признак того, что
func (e envReader) Lookup(key string) (string, bool) {
	value, ok := e.lookup(key)
	if !ok || value == "" {
		return "", false
	}

	return value, true
}

// String возвращает значение переменной окружения или fallback, если
func (e envReader) String(key, fallback string) string {
	if value, ok := e.Lookup(key); ok {
		return value
	}

	return fallback
}
