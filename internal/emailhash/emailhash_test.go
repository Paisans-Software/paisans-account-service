package emailhash

import (
	"bytes"
	"testing"
)

func TestHashNormalises(t *testing.T) {
	k := bytes.Repeat([]byte{1}, 32)
	if !bytes.Equal(Sum(k, " Bob@Example.org "), Sum(k, "bob@example.org")) {
		t.Fatal("trim and lower-case must not change the hash")
	}
	if bytes.Equal(Sum(k, "bob@example.org"), Sum(bytes.Repeat([]byte{2}, 32), "bob@example.org")) {
		t.Fatal("the key must change the hash")
	}
}
