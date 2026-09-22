package protocol

import "fmt"

// ReadEntityMotion decodes one velocity vector in the selected version's
// wire format. Both encodings describe blocks travelled during one client
// tick.
func ReadEntityMotion(r *Reader, encoding EntityMotionEncoding) (float64, float64, float64) {
	switch encoding {
	case EntityMotionLegacy:
		return float64(r.I16()) / 8000.0,
			float64(r.I16()) / 8000.0,
			float64(r.I16()) / 8000.0
	case EntityMotionLowPrecision:
		return readLowPrecisionVector(r)
	default:
		r.Fail(fmt.Errorf("protocol: unknown entity motion encoding %d", encoding))
		return 0, 0, 0
	}
}

func readLowPrecisionVector(r *Reader) (float64, float64, float64) {
	lowest := r.U8()
	if lowest == 0 {
		return 0, 0, 0
	}
	middle := r.U8()
	highest := uint64(uint32(r.I32()))
	packed := highest<<16 | uint64(middle)<<8 | uint64(lowest)
	scale := uint64(lowest & 3)
	if lowest&4 != 0 {
		scale |= uint64(uint32(r.VarInt())) << 2
	}
	unpack := func(value uint64) float64 {
		quantized := value & 32767
		if quantized > 32766 {
			quantized = 32766
		}
		return float64(quantized)*2.0/32766.0 - 1.0
	}
	multiplier := float64(scale)
	return unpack(packed>>3) * multiplier,
		unpack(packed>>18) * multiplier,
		unpack(packed>>33) * multiplier
}
