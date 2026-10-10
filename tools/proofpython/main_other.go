//go:build !windows

package main

import "errors"

func platformMain(_ string, _ []string) (int, error) {
	return 2, errors.New("proof Python provider requires native Windows AMD64")
}
