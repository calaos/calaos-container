package models

import (
	"reflect"
	"testing"

	"github.com/calaos/calaos-container/models/structs"
)

func TestParseDpkgQueryOutput(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want structs.ImageMap
	}{
		{
			name: "normal multi-line output",
			in: "calaos-base\t3.4.1\tinstalled\n" +
				"calaos-server\t3.4.0\tinstalled\n",
			want: structs.ImageMap{
				"calaos-base":   structs.Image{Name: "calaos-base", Version: "3.4.1"},
				"calaos-server": structs.Image{Name: "calaos-server", Version: "3.4.0"},
			},
		},
		{
			name: "empty string",
			in:   "",
			want: structs.ImageMap{},
		},
		{
			name: "non-installed status is skipped",
			in: "calaos-base\t3.4.1\tinstalled\n" +
				"calaos-old\t3.0.0\tconfig-files\n",
			want: structs.ImageMap{
				"calaos-base": structs.Image{Name: "calaos-base", Version: "3.4.1"},
			},
		},
		{
			name: "malformed line missing fields is skipped",
			in: "calaos-base\t3.4.1\tinstalled\n" +
				"calaos-broken\tinstalled\n",
			want: structs.ImageMap{
				"calaos-base": structs.Image{Name: "calaos-base", Version: "3.4.1"},
			},
		},
		{
			name: "trailing newline",
			in:   "calaos-base\t3.4.1\tinstalled\n",
			want: structs.ImageMap{
				"calaos-base": structs.Image{Name: "calaos-base", Version: "3.4.1"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseDpkgQueryOutput(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseDpkgQueryOutput(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}
