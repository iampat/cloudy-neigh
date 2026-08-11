package sample

import "testing"

func TestAdd(t *testing.T) {
	tests := []struct {
		name string
		a, b int
		want int
	}{
		{name: "zero", a: 0, b: 0, want: 0},
		{name: "positive", a: 2, b: 3, want: 5},
		{name: "negative", a: -2, b: -3, want: -5},
		{name: "mixed signs", a: -2, b: 3, want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Add(tt.a, tt.b); got != tt.want {
				t.Errorf("Add(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}
