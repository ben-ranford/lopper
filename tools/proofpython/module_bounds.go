package main

import "errors"

func moduleCount(needed, width uint32) (uint32, error) {
	if width == 0 || width > 8 || needed == 0 || needed%width != 0 || needed/width > maxModules {
		return 0, errors.New("partial or oversized module snapshot")
	}
	return needed / width, nil
}

func modulePathLength(length uintptr) error {
	if length == 0 || length >= maxPathUnits {
		return errors.New("missing or truncated module path")
	}
	return nil
}
