package handlers

import (
	"reflect"
	"testing"
)

func TestWSOriginPatterns(t *testing.T) {
	got := wsOriginPatterns([]string{
		"https://riderapp.codevertexafrica.com",
		"http://localhost:3000",
		" https://riderapp.codevertexafrica.com ",
		"*.codevertexafrica.com",
		"",
		"://bad",
	})
	want := []string{"riderapp.codevertexafrica.com", "localhost:3000", "*.codevertexafrica.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got := wsOriginPatterns(nil); !reflect.DeepEqual(got, []string{"*"}) {
		t.Fatalf("empty list: got %v, want [*]", got)
	}
}
