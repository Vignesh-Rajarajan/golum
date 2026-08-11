package prompt

import (
	"reflect"
	"testing"
)

func TestParseCommandArgsAndSubstitute(t *testing.T) {
	args, err := ParseCommandArgs(`one "two words" 'three words' four\ five`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"one", "two words", "three words", "four five"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args=%v want %v", args, want)
	}
	got := SubstituteArgs(`first=$1 second=$2 all=$ARGUMENTS`, args)
	if got != `first=one second=two words all=one two words three words four five` {
		t.Fatalf("substitution=%q", got)
	}
}
