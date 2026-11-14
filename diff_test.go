package documents

import (
	"strings"
	"testing"
)

// MOD-DOC-33 compare: a one-line change marks one added and one removed line.
func TestDiffOneLine(t *testing.T) {
	a := splitLines("# Motion\n\nThe petitioner asks the court.\nSigned.\n")
	b := splitLines("# Motion\n\nThe petitioner moves the court.\nSigned.\n")
	var add, del, same int
	for _, l := range diffLines(a, b) {
		switch l.Op {
		case "+":
			add++
			if l.Text != "The petitioner moves the court." {
				t.Errorf("added %q", l.Text)
			}
		case "-":
			del++
			if l.Text != "The petitioner asks the court." {
				t.Errorf("removed %q", l.Text)
			}
		default:
			same++
		}
	}
	if add != 1 || del != 1 || same != 3 {
		t.Fatalf("add %d del %d same %d", add, del, same)
	}
}

func TestDiffEdges(t *testing.T) {
	if got := diffLines(nil, nil); len(got) != 0 {
		t.Fatalf("empty: %v", got)
	}
	got := diffLines(nil, []string{"x", "y"})
	if len(got) != 2 || got[0].Op != "+" || got[1].Op != "+" {
		t.Fatalf("all added: %v", got)
	}
	got = diffLines([]string{"a", "b", "c"}, []string{"a", "c"})
	var ops []string
	for _, l := range got {
		ops = append(ops, l.Op+l.Text)
	}
	if strings.Join(ops, ",") != " a,-b, c" {
		t.Fatalf("got %v", ops)
	}
	if lines := splitLines("a\r\nb"); len(lines) != 2 || lines[1] != "b" {
		t.Fatalf("crlf: %q", lines)
	}
}
