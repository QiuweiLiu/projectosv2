package objective

import "testing"

func TestLoadAndParseRejectEmptyInput(t *testing.T) {
	if _, err := Load(""); err == nil {
		t.Fatal("Load(\"\") must fail")
	}
	if _, err := Parse(nil); err == nil {
		t.Fatal("Parse(nil) must fail")
	}
	if _, err := Parse([]byte("")); err == nil {
		t.Fatal("Parse(empty) must fail")
	}
	if _, err := Parse([]byte("null")); err == nil {
		t.Fatal("Parse(null) must fail")
	}
}
