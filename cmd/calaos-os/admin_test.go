package main

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func noTTY() ([]byte, error) { return nil, errors.New("not a terminal") }

func TestReadNewPasswordFromPipe(t *testing.T) {
	cases := []struct {
		in, want string
		wantErr  bool
	}{
		{in: "correct horse battery\n", want: "correct horse battery"},
		{in: "correct horse battery", want: "correct horse battery"},
		{in: "crlf password!\r\n", want: "crlf password!"},
		{in: "  spaces kept  \nsecond line ignored\n", want: "  spaces kept  "},
		{in: "mot de passe très long\n", want: "mot de passe très long"},
		{in: strings.Repeat("x", adminMaxBytes) + "\n", want: strings.Repeat("x", adminMaxBytes)},
		{in: strings.Repeat("x", adminMaxBytes+1) + "\n", wantErr: true},
		{in: strings.Repeat("x", 10*adminInputBuffer), wantErr: true},
		{in: "\n", wantErr: true},
		{in: "", wantErr: true},
	}
	for _, c := range cases {
		var prompt strings.Builder
		got, err := readNewPassword(strings.NewReader(c.in), &prompt, false, noTTY)
		if c.wantErr {
			if err == nil {
				t.Errorf("%.20q: accepted %q", c.in, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%.20q: got %q, %v", c.in, got, err)
		}
		if prompt.Len() != 0 {
			t.Errorf("prompt printed for piped input: %q", prompt.String())
		}
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestReadNewPasswordReadError(t *testing.T) {
	if _, err := readNewPassword(failingReader{}, io.Discard, false, noTTY); err == nil {
		t.Fatal("read error ignored")
	}
}

func TestReadNewPasswordOnTerminal(t *testing.T) {
	answers := func(a ...string) func() ([]byte, error) {
		return func() ([]byte, error) {
			if len(a) == 0 {
				return nil, io.EOF
			}
			v := a[0]
			a = a[1:]
			return []byte(v), nil
		}
	}
	stdin := strings.NewReader("must not be read\n")

	var prompt strings.Builder
	got, err := readNewPassword(stdin, &prompt, true, answers("correct horse battery", "correct horse battery"))
	if err != nil || got != "correct horse battery" {
		t.Fatalf("got %q, %v", got, err)
	}
	if strings.Count(prompt.String(), "password") != 2 || strings.Contains(prompt.String(), "correct") {
		t.Fatalf("prompt %q", prompt.String())
	}
	if stdin.Len() == 0 {
		t.Fatal("stdin read instead of the no-echo reader")
	}

	for name, ask := range map[string]func() ([]byte, error){
		"mismatch":  answers("correct horse battery", "correct horse batterY"),
		"empty":     answers("", ""),
		"too long":  answers(strings.Repeat("x", adminMaxBytes+1), strings.Repeat("x", adminMaxBytes+1)),
		"eof first": answers(),
		"eof twice": answers("correct horse battery"),
	} {
		if got, err := readNewPassword(stdin, io.Discard, true, ask); err == nil {
			t.Errorf("%s: accepted %q", name, got)
		}
	}
}
