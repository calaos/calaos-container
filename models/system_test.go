package models

import (
	"strings"
	"testing"
)

func TestParseCPUUsagePercent(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    int
		wantErr bool
	}{
		{
			name: "mostly idle host reports low usage",
			// user=100 nice=0 system=50 idle=9850 iowait=0 irq=0 softirq=0 steal=0
			content: "cpu  100 0 50 9850 0 0 0 0 0 0\ncpu0 100 0 50 9850 0 0 0 0 0 0\n",
			want:    1, // (100+0+50)*100/10000 = 1.5 -> truncated to 1
		},
		{
			name: "busy host reports high usage",
			// user=8000 nice=0 system=1000 idle=1000 iowait=0
			content: "cpu  8000 0 1000 1000 0 0 0 0 0 0\n",
			want:    90, // (8000+1000)*100/10000 = 90
		},
		{
			name: "idle is excluded from used, not counted towards it",
			// all time is idle: used must be 0, not 100.
			content: "cpu  0 0 0 10000 0 0 0 0 0 0\n",
			want:    0,
		},
		{
			name: "iowait is excluded from used",
			// user=0 nice=0 system=0 idle=0 iowait=10000 -> fully waiting on IO, not "used"
			content: "cpu  0 0 0 0 10000 0 0 0 0 0\n",
			want:    0,
		},
		{
			name: "irq/softirq/steal count as used",
			// user=0 nice=0 system=0 idle=0 iowait=0 irq=250 softirq=250 steal=500
			content: "cpu  0 0 0 0 0 250 250 500 0 0\n",
			want:    100,
		},
		{
			name:    "missing iowait field (old kernel) still works",
			content: "cpu  100 0 50 9850\n",
			want:    1,
		},
		{
			name:    "no aggregate cpu line",
			content: "cpu0 100 0 50 9850 0 0 0 0 0 0\n",
			wantErr: true,
		},
		{
			name:    "empty input",
			content: "",
			wantErr: true,
		},
		{
			name:    "malformed numeric field returns error, not a silent 0",
			content: "cpu  100 0 abc 9850 0 0 0 0 0 0\n",
			wantErr: true,
		},
		{
			name:    "too few fields to identify idle",
			content: "cpu  100 0\n",
			wantErr: true,
		},
		{
			name:    "all-zero fields returns error rather than a meaningless 0%",
			content: "cpu  0 0 0 0 0 0 0 0 0 0\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCPUUsagePercent(tt.content)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseCPUUsagePercent() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got != tt.want {
				t.Errorf("parseCPUUsagePercent() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestCPUUsageFromFields(t *testing.T) {
	tests := []struct {
		name    string
		fields  []string
		want    int
		wantErr bool
	}{
		{
			name:   "standard fields",
			fields: strings.Fields("100 0 50 9850 0 0 0 0 0 0"),
			want:   1,
		},
		{
			name:    "empty fields",
			fields:  []string{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := cpuUsageFromFields(tt.fields)
			if (err != nil) != tt.wantErr {
				t.Fatalf("cpuUsageFromFields() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got != tt.want {
				t.Errorf("cpuUsageFromFields() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestParseMemoryUsagePercent(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    int
		wantErr bool
	}{
		{
			name: "normal meminfo",
			content: "MemTotal:       10000000 kB\n" +
				"MemFree:         2000000 kB\n" +
				"MemAvailable:    4000000 kB\n" +
				"Buffers:          500000 kB\n",
			want: 60, // (10000000-4000000)*100/10000000
		},
		{
			name: "fully idle host, most memory available",
			content: "MemTotal:       10000000 kB\n" +
				"MemAvailable:    9800000 kB\n",
			want: 2,
		},
		{
			name:    "missing MemTotal returns error, not a silent 0",
			content: "MemAvailable:    4000000 kB\n",
			wantErr: true,
		},
		{
			name:    "missing MemAvailable returns error, not a silent 0",
			content: "MemTotal:       10000000 kB\n",
			wantErr: true,
		},
		{
			name:    "MemTotal zero returns error",
			content: "MemTotal:       0 kB\nMemAvailable:    0 kB\n",
			wantErr: true,
		},
		{
			name:    "malformed MemTotal value returns error, not a silent 0",
			content: "MemTotal:       notanumber kB\nMemAvailable:    4000000 kB\n",
			wantErr: true,
		},
		{
			name:    "malformed MemAvailable value returns error, not a silent 0",
			content: "MemTotal:       10000000 kB\nMemAvailable:    notanumber kB\n",
			wantErr: true,
		},
		{
			name:    "empty input",
			content: "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseMemoryUsagePercent(tt.content)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseMemoryUsagePercent() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got != tt.want {
				t.Errorf("parseMemoryUsagePercent() = %d, want %d", got, tt.want)
			}
		})
	}
}
