package testsetup

import (
	"net"
	"testing"
)

func TestReserveLoopbackPortHoldsAddressUntilReleased(t *testing.T) {
	reservation := ReserveLoopbackPort(t)
	if listener, err := net.Listen("tcp", reservation.Address); err == nil {
		_ = listener.Close()
		t.Fatalf("reserved loopback port %q was not held", reservation.Address)
	}

	reservation.Release()
	listener, err := net.Listen("tcp", reservation.Address)
	if err != nil {
		t.Fatalf("bind released loopback port %q: %v", reservation.Address, err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("close released loopback port %q: %v", reservation.Address, err)
	}
}
