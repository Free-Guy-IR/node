//go:build !linux

package l2tp

import "errors"

func checkHostPrereqs() error {
	return errors.New("l2tp is only supported on linux")
}

func applyHostRouting(_, _, _ string, _ func(string, ...any)) (func(), error) {
	return nil, errors.New("l2tp host routing is only supported on linux")
}

func requireIPsecEnabled() bool { return false }

func ensureIPsecGuard(_ string) error {
	return errors.New("l2tp ipsec guard is only supported on linux")
}

func removeIPsecGuard(_ string) error { return nil }

func ipsecGuardSupported() error { return errors.New("l2tp ipsec guard is only supported on linux") }
