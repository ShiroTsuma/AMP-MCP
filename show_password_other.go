//go:build !windows

package main

import "errors"

func showUserPassword(string) error { return errors.New("credential viewer requires Windows") }
