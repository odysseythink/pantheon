package localonnx

// Minimal protobuf walker that extracts graph.input / graph.output names
// from an ONNX ModelProto. This avoids dragging an allocator through the ORT
// C API just to read session IO names. ONNX files produced by the fastembed
// ecosystem are standard ModelProtos, which is all this supports.

import (
	"errors"
	"fmt"
)

type onnxIO struct {
	Inputs  []string
	Outputs []string
}

func onnxGraphIONames(model []byte) (onnxIO, error) {
	var out onnxIO
	// ModelProto.graph == field 7 (LEN).
	foundGraph := false
	err := walkFields(model, func(field int, wire int, data []byte, val uint64) error {
		if field == 7 && wire == 2 {
			foundGraph = true
			// GraphProto.input == 11 (LEN), output == 12 (LEN).
			return walkFields(data, func(field int, wire int, data []byte, _ uint64) error {
				if wire != 2 {
					return nil
				}
				switch field {
				case 11:
					name, err := valueInfoName(data)
					if err != nil {
						return err
					}
					out.Inputs = append(out.Inputs, name)
				case 12:
					name, err := valueInfoName(data)
					if err != nil {
						return err
					}
					out.Outputs = append(out.Outputs, name)
				}
				return nil
			})
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	if !foundGraph {
		return out, errors.New("onnx: no graph field in ModelProto")
	}
	if len(out.Inputs) == 0 || len(out.Outputs) == 0 {
		return out, fmt.Errorf("onnx: graph has %d inputs / %d outputs", len(out.Inputs), len(out.Outputs))
	}
	return out, nil
}

// valueInfoName reads ValueInfoProto.name == field 1 (LEN).
func valueInfoName(data []byte) (string, error) {
	var name string
	err := walkFields(data, func(field int, wire int, data []byte, _ uint64) error {
		if field == 1 && wire == 2 {
			name = string(data)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if name == "" {
		return "", errors.New("onnx: ValueInfoProto without a name")
	}
	return name, nil
}

// walkFields iterates the top-level protobuf fields of buf.
// wire types: 0=varint, 1=64bit, 2=LEN, 5=32bit (groups are not produced by
// modern ONNX exporters and are rejected).
func walkFields(buf []byte, fn func(field, wire int, data []byte, val uint64) error) error {
	pos := 0
	for pos < len(buf) {
		key, n := readVarint(buf[pos:])
		if n <= 0 {
			return errors.New("onnx: truncated protobuf key")
		}
		pos += n
		field := int(key >> 3)
		wire := int(key & 7)
		switch wire {
		case 0:
			v, n := readVarint(buf[pos:])
			if n <= 0 {
				return errors.New("onnx: truncated varint")
			}
			pos += n
			if err := fn(field, wire, nil, v); err != nil {
				return err
			}
		case 1:
			if pos+8 > len(buf) {
				return errors.New("onnx: truncated fixed64")
			}
			pos += 8
		case 2:
			l, n := readVarint(buf[pos:])
			if n <= 0 || pos+n+int(l) > len(buf) {
				return errors.New("onnx: truncated length-delimited field")
			}
			pos += n
			if err := fn(field, wire, buf[pos:pos+int(l)], 0); err != nil {
				return err
			}
			pos += int(l)
		case 5:
			if pos+4 > len(buf) {
				return errors.New("onnx: truncated fixed32")
			}
			pos += 4
		default:
			return fmt.Errorf("onnx: unsupported wire type %d", wire)
		}
	}
	return nil
}

func readVarint(buf []byte) (uint64, int) {
	var v uint64
	var shift uint
	for i, b := range buf {
		if i >= 10 {
			return 0, -1
		}
		v |= uint64(b&0x7F) << shift
		if b < 0x80 {
			return v, i + 1
		}
		shift += 7
	}
	return 0, -1
}
