package arxiv

import "testing"

func TestSplitVersion(t *testing.T) {
	tests := []struct {
		in      string
		id      string
		version int
		ok      bool
	}{
		{"http://arxiv.org/abs/2307.08691v2", "2307.08691", 2, true},
		{"https://arxiv.org/abs/2401.12345v12", "2401.12345", 12, true},
		{" 2401.12345v3\n", "2401.12345", 3, true},
		{"http://arxiv.org/abs/2401.12345", "2401.12345", 0, false},
		{"http://arxiv.org/abs/2401.12345v", "2401.12345v", 0, false},
		{"http://arxiv.org/abs/2401.12345v0", "2401.12345", 0, false},
		{"http://arxiv.org/abs/2401.12345vx", "2401.12345vx", 0, false},
		{"http://arxiv.org/abs/2401.12345v99999999999999999999", "2401.12345", 0, false},
		{"", "", 0, false},
	}
	for _, tt := range tests {
		id, v, ok := SplitVersion(tt.in)
		if id != tt.id || v != tt.version || ok != tt.ok {
			t.Errorf("SplitVersion(%q) = (%q, %d, %v), want (%q, %d, %v)", tt.in, id, v, ok, tt.id, tt.version, tt.ok)
		}
	}
}

func TestPDFURL(t *testing.T) {
	if got, want := PDFURL("2307.08691", 2), "https://arxiv.org/pdf/2307.08691v2.pdf"; got != want {
		t.Errorf("PDFURL = %q, want %q", got, want)
	}
}
