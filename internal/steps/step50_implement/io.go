package step50_implement

import internalio "github.com/nishimoto265/auto-improve/internal/io"

func writeAtomicImpl(path string, data []byte) error {
	return internalio.WriteAtomic(path, data)
}

func writeJSONAtomicImpl(path string, v any) error {
	return internalio.WriteJSONAtomic(path, v)
}

func readJSON[T any](path string) (T, error) {
	return internalio.ReadJSON[T](path)
}

func writeAtomic(path string, data []byte) error {
	return writeAtomicImpl(path, data)
}

func writeJSONAtomic(path string, v any) error {
	return writeJSONAtomicImpl(path, v)
}
