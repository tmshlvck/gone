package htmx

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestRequestClassification(t *testing.T) {
	r := httptest.NewRequest("GET", "/x", nil)
	if IsRequest(r) {
		t.Fatal("plain request must not be HX")
	}
	r.Header.Set("HX-Request", "true")
	r.Header.Set("HX-Boosted", "true")
	r.Header.Set("HX-Target", "hero-body")
	r.Header.Set("HX-Trigger-Name", "q")
	r.Header.Set("HX-Trigger", "search-box")
	if !IsRequest(r) || !IsBoosted(r) {
		t.Fatal("HX headers not detected")
	}
	if Target(r) != "hero-body" || TriggerName(r) != "q" || TriggerID(r) != "search-box" {
		t.Fatalf("header readers wrong: %q %q %q", Target(r), TriggerName(r), TriggerID(r))
	}
}

func TestCurrentURL(t *testing.T) {
	r := httptest.NewRequest("POST", "/heroes/1/edit", nil)
	if _, ok := CurrentURL(r); ok {
		t.Fatal("absent HX-Current-URL must report ok=false")
	}
	r.Header.Set("HX-Current-URL", "http://host/admin/heroes?page=3&sort=power")
	u, ok := CurrentURL(r)
	if !ok {
		t.Fatal("present HX-Current-URL must parse")
	}
	if u.Query().Get("page") != "3" || u.Query().Get("sort") != "power" {
		t.Fatalf("query not recovered: %v", u.Query())
	}
}

func TestReplyDirectives(t *testing.T) {
	w := httptest.NewRecorder()
	Reply().
		Retarget("#table").
		Reswap("innerHTML").
		Reselect("#rows").
		PushURL("/admin/heroes?page=2").
		Apply(w)

	h := w.Header()
	if h.Get("HX-Retarget") != "#table" {
		t.Errorf("HX-Retarget = %q", h.Get("HX-Retarget"))
	}
	if h.Get("HX-Reswap") != "innerHTML" {
		t.Errorf("HX-Reswap = %q", h.Get("HX-Reswap"))
	}
	if h.Get("HX-Reselect") != "#rows" {
		t.Errorf("HX-Reselect = %q", h.Get("HX-Reselect"))
	}
	if h.Get("HX-Push-Url") != "/admin/heroes?page=2" {
		t.Errorf("HX-Push-Url = %q", h.Get("HX-Push-Url"))
	}
}

func TestEmptyReplyWritesNothing(t *testing.T) {
	w := httptest.NewRecorder()
	Reply().Apply(w)
	for k := range w.Header() {
		t.Errorf("empty Reply set header %q", k)
	}
}

func TestModalTriggersCoalesce(t *testing.T) {
	w := httptest.NewRecorder()
	// A nested L2 create closes its modal and asks every relation widget to
	// refresh — two events in one HX-Trigger header, matching the old
	// hand-written `{"closeModal":…,"refresh-relation":true}`.
	Reply().
		CloseModal("crud-modal-l2").
		Trigger("refresh-relation", true).
		Apply(w)

	raw := w.Header().Get("HX-Trigger")
	var got map[string]any
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("HX-Trigger not valid JSON object: %q (%v)", raw, err)
	}
	if got["closeModal"] != "crud-modal-l2" {
		t.Errorf("closeModal = %v", got["closeModal"])
	}
	if got["refresh-relation"] != true {
		t.Errorf("refresh-relation = %v", got["refresh-relation"])
	}
}

func TestOpenCloseModalEventNames(t *testing.T) {
	w := httptest.NewRecorder()
	Reply().OpenModal("hero-modal-l1").Apply(w)
	var got map[string]any
	_ = json.Unmarshal([]byte(w.Header().Get("HX-Trigger")), &got)
	if got["openModal"] != "hero-modal-l1" {
		t.Fatalf("openModal not emitted: %v", got)
	}
}

func TestRedirectAndRefresh(t *testing.T) {
	w := httptest.NewRecorder()
	Reply().Redirect("/login").Apply(w)
	if w.Header().Get("HX-Redirect") != "/login" {
		t.Errorf("HX-Redirect = %q", w.Header().Get("HX-Redirect"))
	}
	w2 := httptest.NewRecorder()
	Reply().Refresh().Apply(w2)
	if w2.Header().Get("HX-Refresh") != "true" {
		t.Errorf("HX-Refresh = %q", w2.Header().Get("HX-Refresh"))
	}
}
