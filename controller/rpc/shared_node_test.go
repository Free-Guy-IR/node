package rpc

import (
	"errors"
	"net"
	"strconv"
	"syscall"
)

const serviceBindAttempts = 5

func listenOnAnUnusedLoopbackPort(listen func(addr string) error) (string, error) {
	var err error
	for range serviceBindAttempts {
		var probe net.Listener
		if probe, err = net.Listen("tcp", net.JoinHostPort(nodeHost, "0")); err != nil {
			return "", err
		}
		port := probe.Addr().(*net.TCPAddr).Port
		if err = probe.Close(); err != nil {
			return "", err
		}
		addr := net.JoinHostPort(nodeHost, strconv.Itoa(port))
		if err = listen(addr); !errors.Is(err, syscall.EADDRINUSE) {
			return addr, err
		}
	}
	return "", err
}
