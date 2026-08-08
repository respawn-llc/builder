package testsetup

import (
	"fmt"
	"net"
	"strconv"
	"sync"
	"testing"
)

type LoopbackPortReservation struct {
	Host    string
	Port    int
	Address string
}

var loopbackPortReservations = struct {
	sync.Mutex
	listeners map[string]net.Listener
}{
	listeners: make(map[string]net.Listener),
}

func ReserveLoopbackPort(t testing.TB) *LoopbackPortReservation {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve loopback port: %v", err)
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		t.Fatalf("reserved loopback listener address = %T, want *net.TCPAddr", listener.Addr())
	}
	reservation := &LoopbackPortReservation{
		Host:    address.IP.String(),
		Port:    address.Port,
		Address: listener.Addr().String(),
	}

	loopbackPortReservations.Lock()
	if existing := loopbackPortReservations.listeners[reservation.Address]; existing != nil {
		loopbackPortReservations.Unlock()
		_ = listener.Close()
		t.Fatalf("loopback port %q is already reserved", reservation.Address)
	}
	loopbackPortReservations.listeners[reservation.Address] = listener
	loopbackPortReservations.Unlock()
	t.Cleanup(reservation.Release)
	return reservation
}

func (r *LoopbackPortReservation) Release() {
	ReleaseLoopbackAddress(r.Address)
}

func ReleaseLoopbackPort(host string, port int) {
	ReleaseLoopbackAddress(net.JoinHostPort(host, strconv.Itoa(port)))
}

func ReleaseLoopbackAddress(address string) {
	loopbackPortReservations.Lock()
	listener := loopbackPortReservations.listeners[address]
	delete(loopbackPortReservations.listeners, address)
	loopbackPortReservations.Unlock()
	if listener == nil {
		return
	}
	if err := listener.Close(); err != nil {
		panic(fmt.Sprintf("release loopback port %q: %v", address, err))
	}
}
