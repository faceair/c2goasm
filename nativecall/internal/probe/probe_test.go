package probe

import (
	"testing"

	"github.com/faceair/c2goasm/nativecall"
)

func TestCall0(t *testing.T) {
	if got := nativecall.Call0(call0Address()); got != 42 {
		t.Fatalf("Call0 result = %d, want 42", got)
	}
}

func TestCallBytes(t *testing.T) {
	if got := nativecall.CallBytes(callBytesAddress(), []byte{5}); got != 6 {
		t.Fatalf("CallBytes result = %d, want 6", got)
	}
}

func TestZeroAddressPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Call0 accepted a zero function address")
		}
	}()
	nativecall.Call0(0)
}
