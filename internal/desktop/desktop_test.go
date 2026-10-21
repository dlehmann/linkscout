package desktop

import (
	"reflect"
	"testing"
)

func TestCommands(t *testing.T) {
	if got := commands("darwin", "/tmp/x"); !reflect.DeepEqual(got, [][]string{{"open", "/tmp/x"}}) {
		t.Errorf("darwin: %v", got)
	}
	got := commands("linux", "https://example.com/")
	if len(got) != 3 || got[0][0] != "xdg-open" || got[1][0] != "gio" || got[1][1] != "open" {
		t.Errorf("linux: %v", got)
	}
	for _, c := range got {
		if c[len(c)-1] != "https://example.com/" {
			t.Errorf("target must be the last argument: %v", c)
		}
	}
}
