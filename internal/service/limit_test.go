package service

import (
	"net"
	"testing"
)

type fakeConn struct {
	net.Conn
	from string
}

func (c fakeConn) RemoteAddr() net.Addr { a, _ := net.ResolveTCPAddr("tcp", c.from); return a }

// One member cannot take every connection, and all of them together cannot
// take more than the device's cap; a connection given back is room again.
func TestServiceConnectionsAreCapped(t *testing.T) {
	l := newConnLimit(3, 2)
	a, b, c := fakeConn{from: "[fdb0::2]:1"}, fakeConn{from: "[fdb0::3]:1"}, fakeConn{from: "[fdb0::4]:1"}
	fa, ok1 := l.take(a)
	_, ok2 := l.take(a)
	if !ok1 || !ok2 {
		t.Fatal("a member's first two were refused")
	}
	if _, ok := l.take(a); ok {
		t.Fatal("a member got a third, over its cap of two")
	}
	if _, ok := l.take(b); !ok {
		t.Fatal("another member was refused while under the total")
	}
	if _, ok := l.take(c); ok {
		t.Fatal("a fourth connection was taken over the total of three")
	}
	l.give(fa)
	if _, ok := l.take(c); !ok {
		t.Fatal("a connection given back was not room again")
	}
}
