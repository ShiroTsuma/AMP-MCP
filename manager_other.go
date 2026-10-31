//go:build !windows

package main

import "errors"

func openManager() error { return errors.New("connection manager requires Windows") }
