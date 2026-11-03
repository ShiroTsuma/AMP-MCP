//go:build !windows && !linux

package secretstore

func platformStore(app string, opts Options, backend string) (Store, error) {
	if backend == "" {
		return nil, nil // vault
	}
	return nil, ErrUnsupported
}
