package handler

import "testing"

func TestParseUploadContentRange(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		wantStart int64
		wantEnd   int64
		wantTotal int64
		wantErr   bool
	}{
		{name: "valid", value: "bytes 0-4194303/8388608", wantEnd: 4194303, wantTotal: 8388608},
		{name: "valid later part", value: "bytes 4194304-8388607/8388608", wantStart: 4194304, wantEnd: 8388607, wantTotal: 8388608},
		{name: "trailing data", value: "bytes 0-1/2 trailing", wantErr: true},
		{name: "missing unit", value: "0-1/2", wantErr: true},
		{name: "negative", value: "bytes -1-1/2", wantErr: true},
		{name: "end reaches total", value: "bytes 0-2/2", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			start, end, total, err := parseUploadContentRange(test.value)
			if test.wantErr {
				if err == nil {
					t.Fatal("expected parse error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseUploadContentRange() error = %v", err)
			}
			if start != test.wantStart || end != test.wantEnd || total != test.wantTotal {
				t.Fatalf("range = (%d,%d,%d), want (%d,%d,%d)", start, end, total, test.wantStart, test.wantEnd, test.wantTotal)
			}
		})
	}
}
