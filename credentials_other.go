//go:build !windows

package main

import "errors"

func encryptCredential(string) (string, error) {
	return "", errors.New("this version requires Windows DPAPI")
}

func decryptToken(string) (string, error) {
	return "", errors.New("this version requires Windows DPAPI")
}
