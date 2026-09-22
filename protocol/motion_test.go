package protocol

import (
	"math"
	"testing"
)

func TestReadEntityMotionLegacy(t *testing.T) {
	r := NewReader([]byte{0x0f, 0xa0, 0xe0, 0xc0, 0x07, 0xd0})
	x, y, z := ReadEntityMotion(r, EntityMotionLegacy)
	if err := r.Err(); err != nil {
		t.Fatalf("ReadEntityMotion: %v", err)
	}
	if x != 0.5 || y != -1 || z != 0.25 {
		t.Errorf("motion = %v,%v,%v, want 0.5,-1,0.25", x, y, z)
	}
	if left := len(r.Remaining()); left != 0 {
		t.Errorf("%d unread legacy motion bytes", left)
	}
}

func TestReadEntityMotionLowPrecision(t *testing.T) {
	tests := []struct {
		name      string
		wire      []byte
		x, y, z   float64
		tolerance float64
	}{
		{"one-byte zero", []byte{0}, 0, 0, 0, 0},
		{"single-scale vector", []byte{0xf9, 0xff, 0x40, 0x00, 0xff, 0xfe}, 0.5, 0, -0.5, 1e-4},
		{"continued scale", []byte{0xf5, 0xff, 0x9f, 0xfe, 0x80, 0x03, 0x01}, 5, -2.5, 1.25, 1e-3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewReader(tc.wire)
			x, y, z := ReadEntityMotion(r, EntityMotionLowPrecision)
			if err := r.Err(); err != nil {
				t.Fatalf("ReadEntityMotion: %v", err)
			}
			if math.Abs(x-tc.x) > tc.tolerance || math.Abs(y-tc.y) > tc.tolerance || math.Abs(z-tc.z) > tc.tolerance {
				t.Errorf("motion = %v,%v,%v, want %v,%v,%v ± %v", x, y, z, tc.x, tc.y, tc.z, tc.tolerance)
			}
			if left := len(r.Remaining()); left != 0 {
				t.Errorf("%d unread low-precision motion bytes", left)
			}
		})
	}
}

func TestReadEntityMotionRejectsUnknownEncoding(t *testing.T) {
	r := NewReader(nil)
	ReadEntityMotion(r, EntityMotionEncoding(255))
	if r.Err() == nil {
		t.Fatal("unknown motion encoding was accepted")
	}
}
