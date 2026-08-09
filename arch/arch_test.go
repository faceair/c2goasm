package arch

import "testing"

func TestResolveExactArchitectureNames(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "", want: "amd64"},
		{input: "amd64", want: "amd64"},
		{input: "x86_64", want: "amd64"},
		{input: "arm64", want: "arm64"},
		{input: "aarch64", want: "arm64"},
	}
	for _, test := range tests {
		descriptor, err := Resolve(test.input)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", test.input, err)
		}
		if descriptor.Name() != test.want {
			t.Fatalf("Resolve(%q) = %q, want %q", test.input, descriptor.Name(), test.want)
		}
	}
}

func TestResolveRejectsAmbiguousSubstrings(t *testing.T) {
	for _, input := range []string{"notx86", "arm64-x86", "amd64-arm64", "aarch64-linux-gnu"} {
		if _, err := Resolve(input); err == nil {
			t.Errorf("Resolve(%q) unexpectedly succeeded", input)
		}
	}
}
