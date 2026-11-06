package opencodeserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMessagesPageFollowsCursor(t *testing.T) {
	var cursors []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, pass, ok := r.BasicAuth(); !ok || user != "opencode" || pass != "secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("order") != "asc" || r.URL.Query().Get("limit") != "2" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		cursor := r.URL.Query().Get("cursor")
		cursors = append(cursors, cursor)
		page := MessagePage{}
		if cursor == "" {
			page.Data = []Message{{ID: "m1", Type: "user", Text: "one"}, {ID: "m2", Type: "assistant", Text: "two"}}
			page.Cursor.Next = "c2"
		} else {
			page.Data = []Message{{ID: "m3", Type: "user", Text: "three"}}
		}
		_ = json.NewEncoder(w).Encode(page)
	}))
	defer ts.Close()
	s := &Server{baseURL: ts.URL, password: "secret"}
	first, err := s.MessagesPage(context.Background(), "ses-1", 2, "", "asc")
	if err != nil || len(first.Data) != 2 || first.Cursor.Next != "c2" {
		t.Fatalf("first page = %+v %v", first, err)
	}
	second, err := s.MessagesPage(context.Background(), "ses-1", 2, first.Cursor.Next, "asc")
	if err != nil || len(second.Data) != 1 || second.Data[0].ID != "m3" {
		t.Fatalf("second page = %+v %v", second, err)
	}
	if len(cursors) != 2 || cursors[1] != "c2" {
		t.Fatalf("cursor was not sent: %v", cursors)
	}
}
