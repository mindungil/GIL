package taskbug

import (
	"reflect"
	"testing"
)

func TestParseLine_Simple(t *testing.T) {
	got, err := ParseLine("a,b,c")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestParseLine_QuotedComma(t *testing.T) {
	got, err := ParseLine(`a,"b,c",d`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "b,c", "d"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

// This is the failing case. Doubled "" inside quotes should be a
// literal quote character — current code closes the quote early.
func TestParseLine_EscapedQuote(t *testing.T) {
	got, err := ParseLine(`a,"she said ""hi""",b`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a", `she said "hi"`, "b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestParseLine_TrailingComma(t *testing.T) {
	got, err := ParseLine("a,b,")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "b", ""}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestParseLine_Empty(t *testing.T) {
	got, err := ParseLine("")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{""}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestParseLine_Unterminated(t *testing.T) {
	_, err := ParseLine(`a,"oops`)
	if err == nil {
		t.Error("expected error for unterminated quote")
	}
}
