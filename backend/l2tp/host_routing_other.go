//go:build !linux

package l2tp

import "errors"

func checkHostPrereqs() error {
	return errors.New("l2tp is only supported on linux")
}

func applyHostRouting(_, _, _ string, _ func(string, ...any)) (func(), error) {
	return nil, errors.New("l2tp host routing is only supported on linux")
}
