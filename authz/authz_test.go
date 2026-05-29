package authz

import (
	"net/http/httptest"
	"testing"
)

func TestAllowAll(t *testing.T) {
	a := AllowAll{}
	r := httptest.NewRequest("GET", "/", nil)
	for _, can := range []func() bool{
		func() bool { return a.CanList(r) },
		func() bool { return a.CanRead(r) },
		func() bool { return a.CanCreate(r) },
		func() bool { return a.CanUpdate(r) },
		func() bool { return a.CanDelete(r) },
	} {
		if !can() {
			t.Error("AllowAll should permit every action")
		}
	}
}

func TestDenyAll(t *testing.T) {
	a := DenyAll{}
	r := httptest.NewRequest("GET", "/", nil)
	for _, can := range []func() bool{
		func() bool { return a.CanList(r) },
		func() bool { return a.CanRead(r) },
		func() bool { return a.CanCreate(r) },
		func() bool { return a.CanUpdate(r) },
		func() bool { return a.CanDelete(r) },
	} {
		if can() {
			t.Error("DenyAll should reject every action")
		}
	}
}

func TestOrAllow(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	// nil → AllowAll
	if !OrAllow(nil).CanRead(r) {
		t.Error("OrAllow(nil) should permit")
	}
	// non-nil pass-through
	if OrAllow(DenyAll{}).CanRead(r) {
		t.Error("OrAllow(DenyAll) should pass DenyAll through")
	}
}
