//go:build !windows

package main

import "errors"

func replaceFile(string, string) error {
	return errors.New("persistent storage currently requires Windows")
}
func lockFile(string) (func(), error) {
	return nil, errors.New("persistent storage currently requires Windows")
}
