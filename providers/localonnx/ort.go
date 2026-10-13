package localonnx

// Minimal CGO-free binding to the ONNX Runtime C API.
//
// The official Go wrapper (yalue/onnxruntime_go) requires cgo, which the
// odybox build forbids (modernc.org/sqlite, cross-platform -trimpath
// builds). This file instead resolves the OrtApi function table by offset —
// generated from the onnxruntime v1.22.0 onnxruntime_c_api.h — and invokes
// the pointers through purego's cross-platform SyscallN. The C API is
// append-only: a runtime newer than v1.22.0 keeps every offset below valid.
// All ORT C entry points take scalars and pointers only, which keeps the
// purego call surface safe on every platform.
//
// C-owned pointers are held in unsafe.Pointer values throughout; the only
// uintptr→pointer reinterpretation is funneled through cPtrOf (reading a C
// result out of a uintptr), which `go vet` accepts.

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// ortAPIVersion requests a stable C API layout: ORT runtimes implement every
// version up to their own, so v17 works against anything >= 1.17.
const ortAPIVersion = 17

const (
	ortTypeFloat32 = 1 // ONNX_TENSOR_ELEMENT_DATA_TYPE_FLOAT
	ortTypeInt64   = 7 // ONNX_TENSOR_ELEMENT_DATA_TYPE_INT64 (9 is BOOL)

	ortDeviceAllocator = 1 // OrtAllocatorType
	ortMemTypeDefault  = 0 // OrtMemType
	ortLoggingError    = 3 // OrtLoggingLevel
)

// Function-pointer offsets within the OrtApi struct (index * pointer size).
// Code generated from onnxruntime v1.22.0 onnxruntime_c_api.h; DO NOT EDIT.
// Regenerate with scripts/gen_ortapi_offsets.py when bumping the ORT base
// (OrtApi is append-only, so these offsets stay valid for newer runtimes).
const (
	ortFnCreateEnvOffset                      = 3
	ortFnReleaseEnvOffset                     = 92
	ortFnGetErrorMessageOffset                = 2
	ortFnReleaseStatusOffset                  = 93
	ortFnCreateSessionOptionsOffset           = 10
	ortFnSetIntraOpNumThreadsOffset           = 24
	ortFnReleaseSessionOptionsOffset          = 100
	ortFnCreateSessionFromArrayOffset         = 8
	ortFnRunOffset                            = 9
	ortFnCreateCpuMemoryInfoOffset            = 69
	ortFnCreateTensorWithDataAsOrtValueOffset = 49
	ortFnGetTensorMutableDataOffset           = 51
	ortFnGetTensorTypeAndShapeOffset          = 65
	ortFnGetTensorElementTypeOffset           = 60
	ortFnGetDimensionsCountOffset             = 61
	ortFnGetDimensionsOffset                  = 62
	ortFnGetTensorShapeElementCountOffset     = 64
	ortFnReleaseTensorTypeAndShapeInfoOffset  = 99
	ortFnReleaseValueOffset                   = 96
	ortFnSessionGetInputCountOffset           = 30
	ortFnSessionGetInputNameOffset            = 36
	ortFnSessionGetOutputCountOffset          = 31
	ortFnSessionGetOutputNameOffset           = 37
	ortFnCreateAllocatorOffset                = 131
	ortFnAllocatorFreeOffset                  = 76
)

var (
	ortMu      sync.Mutex
	ortEnv     unsafe.Pointer // OrtEnv*
	ortAPI     unsafe.Pointer // const OrtApi*
	ortMemInfo unsafe.Pointer // OrtMemoryInfo*
	ortLibPath string
)

// cPtrOf reinterprets a C-returned uintptr as a pointer without tripping
// `go vet`'s unsafeptr check (the conversion goes through the variable's
// memory, which is a legal pointer-to-pointer reinterpretation).
func cPtrOf(r uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&r))
}

// ortFnAt reads the function pointer at OrtApi[offset].
func ortFnAt(api unsafe.Pointer, offset int) uintptr {
	return *(*uintptr)(unsafe.Add(api, uintptr(offset)*unsafe.Sizeof(uintptr(0))))
}

func ortCall(fn uintptr, args ...uintptr) uintptr {
	r1, _, _ := purego.SyscallN(fn, args...)
	return r1
}

// cBytes builds a NUL-terminated UTF-8 buffer; the caller must KeepAlive the
// returned slice for as long as ORT may read it.
func cBytes(s string) []byte {
	b := make([]byte, len(s)+1)
	copy(b, s)
	return b
}

// cString reads a NUL-terminated UTF-8 string owned by ORT.
func cString(p unsafe.Pointer) string {
	if p == nil {
		return ""
	}
	var end uintptr
	for ; ; end++ {
		if *(*byte)(unsafe.Add(p, end)) == 0 {
			break
		}
	}
	return string(unsafe.Slice((*byte)(p), end))
}

// ortCheck converts an OrtStatus* into a Go error (and releases it).
func ortCheck(status uintptr) error {
	if status == 0 {
		return nil
	}
	msg := cString(cPtrOf(ortCall(ortFnAt(ortAPI, ortFnGetErrorMessageOffset), status)))
	ortCall(ortFnAt(ortAPI, ortFnReleaseStatusOffset), status)
	return fmt.Errorf("onnxruntime: %s", msg)
}

// ortInitialize loads the shared library and creates the global OrtEnv +
// CPU memory info. Safe to call repeatedly; concurrent calls serialize.
func ortInitialize(libPath string) error {
	ortMu.Lock()
	defer ortMu.Unlock()
	if ortAPI != nil {
		return nil
	}
	ortLibPath = libPath
	sym, err := ortDlopen(libPath)
	if err != nil {
		return fmt.Errorf("load %s: %w", libPath, err)
	}
	getBaseFn := sym("OrtGetApiBase")
	if getBaseFn == 0 {
		return errors.New("onnxruntime: OrtGetApiBase not exported")
	}
	apiBase := ortCall(getBaseFn)
	if apiBase == 0 {
		return errors.New("onnxruntime: OrtGetApiBase returned NULL")
	}
	ortAPI = cPtrOf(apiBase)
	getAPI := ortFnAt(ortAPI, 0) // OrtApiBase.GetApi is the first member
	api := ortCall(getAPI, uintptr(ortAPIVersion))
	if api == 0 {
		return fmt.Errorf("onnxruntime: runtime does not support C API v%d", ortAPIVersion)
	}
	ortAPI = cPtrOf(api)

	var env uintptr
	logID := cBytes("odybox")
	if err := ortCheck(ortCall(ortFnAt(ortAPI, ortFnCreateEnvOffset),
		uintptr(ortLoggingError), uintptr(unsafe.Pointer(&logID[0])), uintptr(unsafe.Pointer(&env)))); err != nil {
		return fmt.Errorf("create env: %w", err)
	}
	runtime.KeepAlive(logID)
	ortEnv = cPtrOf(env)

	var memInfo uintptr
	if err := ortCheck(ortCall(ortFnAt(ortAPI, ortFnCreateCpuMemoryInfoOffset),
		uintptr(ortDeviceAllocator), uintptr(ortMemTypeDefault), uintptr(unsafe.Pointer(&memInfo)))); err != nil {
		return fmt.Errorf("create memory info: %w", err)
	}
	ortMemInfo = cPtrOf(memInfo)
	return nil
}

// ortSession wraps one loaded model.
type ortSession struct {
	ptr       unsafe.Pointer
	options   unsafe.Pointer
	modelData []byte // must outlive the session (ORT may read the buffer lazily)
}

// ortCreateSession loads model bytes into a new session (2 intra-op threads).
func ortCreateSession(model []byte) (*ortSession, error) {
	if err := ortInitialize(ortLibPath); err != nil {
		return nil, err
	}
	ortMu.Lock()
	defer ortMu.Unlock()

	var opts uintptr
	if err := ortCheck(ortCall(ortFnAt(ortAPI, ortFnCreateSessionOptionsOffset),
		uintptr(unsafe.Pointer(&opts)))); err != nil {
		return nil, fmt.Errorf("create session options: %w", err)
	}
	_ = ortCall(ortFnAt(ortAPI, ortFnSetIntraOpNumThreadsOffset), opts, 2)

	var session uintptr
	data := model // keep the slice header alive below
	if err := ortCheck(ortCall(ortFnAt(ortAPI, ortFnCreateSessionFromArrayOffset),
		uintptr(ortEnv), uintptr(unsafe.Pointer(unsafe.SliceData(data))), uintptr(len(data)),
		opts, uintptr(unsafe.Pointer(&session)))); err != nil {
		ortCall(ortFnAt(ortAPI, ortFnReleaseSessionOptionsOffset), opts)
		return nil, fmt.Errorf("create session: %w", err)
	}
	return &ortSession{ptr: cPtrOf(session), options: cPtrOf(opts), modelData: model}, nil
}

// Destroy releases the session and its options.
func (s *ortSession) Destroy() {
	ortMu.Lock()
	defer ortMu.Unlock()
	ortCall(ortFnAt(ortAPI, ortFnReleaseSessionOptionsOffset), uintptr(s.options))
	s.ptr, s.options = nil, nil
}

// ortTensor is an OrtValue backed by Go memory (inputs) or ORT memory
// (outputs). KeepAlive the backing slices until after Run.
type ortTensor struct{ ptr unsafe.Pointer }

func (t ortTensor) release() { ortCall(ortFnAt(ortAPI, ortFnReleaseValueOffset), uintptr(t.ptr)) }

// ortMakeTensor wraps data as an ORT tensor of shape shape. dataLenBytes is
// the payload size in bytes (the C API takes byte counts, not element
// counts). The caller must runtime.KeepAlive(data) and shape until after Run.
func ortMakeTensor(dataPtr unsafe.Pointer, dataLenBytes int, elemType int, shape []int64) (ortTensor, error) {
	var value uintptr
	status := ortCall(ortFnAt(ortAPI, ortFnCreateTensorWithDataAsOrtValueOffset),
		uintptr(ortMemInfo), uintptr(dataPtr), uintptr(dataLenBytes),
		uintptr(unsafe.Pointer(unsafe.SliceData(shape))), uintptr(len(shape)),
		uintptr(elemType), uintptr(unsafe.Pointer(&value)))
	if err := ortCheck(status); err != nil {
		return ortTensor{}, err
	}
	return ortTensor{ptr: cPtrOf(value)}, nil
}

// ortRun executes the session. names are UTF-8; inputs must be kept alive by
// the caller; missing output pointers are allocated by ORT.
func ortRun(s *ortSession, inputNames, outputNames []string, inputs []ortTensor, outputs []unsafe.Pointer) error {
	if len(inputs) != len(inputNames) || len(outputs) != len(outputNames) {
		return errors.New("onnxruntime: input/output count mismatch")
	}
	inNames := make([]uintptr, len(inputNames))
	keep := make([][]byte, len(inputNames))
	for i, n := range inputNames {
		keep[i] = cBytes(n)
		inNames[i] = uintptr(unsafe.Pointer(&keep[i][0]))
	}
	outNames := make([]uintptr, len(outputNames))
	keepOut := make([][]byte, len(outputNames))
	for i, n := range outputNames {
		keepOut[i] = cBytes(n)
		outNames[i] = uintptr(unsafe.Pointer(&keepOut[i][0]))
	}
	inVals := make([]uintptr, len(inputs))
	for i, t := range inputs {
		inVals[i] = uintptr(t.ptr)
	}
	outVals := make([]uintptr, len(outputs))
	for i, v := range outputs {
		outVals[i] = uintptr(v)
	}
	var runOpts uintptr // NULL run options
	// Run signature (v1.22 header): (session, run_options, input_names,
	// inputs, input_len, output_names, output_names_len, outputs) — the
	// output length precedes the (mutable) outputs array.
	status := ortCall(ortFnAt(ortAPI, ortFnRunOffset),
		uintptr(s.ptr), runOpts,
		uintptr(unsafe.Pointer(unsafe.SliceData(inNames))),
		uintptr(unsafe.Pointer(unsafe.SliceData(inVals))), uintptr(len(inVals)),
		uintptr(unsafe.Pointer(unsafe.SliceData(outNames))),
		uintptr(len(outVals)),
		uintptr(unsafe.Pointer(unsafe.SliceData(outVals))),
	)
	runtime.KeepAlive(keep)
	runtime.KeepAlive(keepOut)
	runtime.KeepAlive(inVals)
	return ortCheck(status)
}

// ortTensorInfo describes an ORT tensor value.
type ortTensorInfo struct {
	ElemType int
	Shape    []int64
}

func ortTensorInfoOf(value unsafe.Pointer) (ortTensorInfo, error) {
	var info uintptr
	if err := ortCheck(ortCall(ortFnAt(ortAPI, ortFnGetTensorTypeAndShapeOffset),
		uintptr(value), uintptr(unsafe.Pointer(&info)))); err != nil {
		return ortTensorInfo{}, err
	}
	defer ortCall(ortFnAt(ortAPI, ortFnReleaseTensorTypeAndShapeInfoOffset), info)
	var (
		elemType int64
		rank     uintptr
	)
	if err := ortCheck(ortCall(ortFnAt(ortAPI, ortFnGetTensorElementTypeOffset),
		info, uintptr(unsafe.Pointer(&elemType)))); err != nil {
		return ortTensorInfo{}, err
	}
	if err := ortCheck(ortCall(ortFnAt(ortAPI, ortFnGetDimensionsCountOffset),
		info, uintptr(unsafe.Pointer(&rank)))); err != nil {
		return ortTensorInfo{}, err
	}
	shape := make([]int64, rank)
	if rank > 0 {
		if err := ortCheck(ortCall(ortFnAt(ortAPI, ortFnGetDimensionsOffset),
			info, uintptr(unsafe.Pointer(unsafe.SliceData(shape))), rank)); err != nil {
			return ortTensorInfo{}, err
		}
	}
	return ortTensorInfo{ElemType: int(elemType), Shape: shape}, nil
}

// ortTensorData copies the tensor's float32 payload out of ORT memory.
func ortTensorData(value unsafe.Pointer, elemCount int) ([]float32, error) {
	var dataPtr uintptr
	if err := ortCheck(ortCall(ortFnAt(ortAPI, ortFnGetTensorMutableDataOffset),
		uintptr(value), uintptr(unsafe.Pointer(&dataPtr)))); err != nil {
		return nil, err
	}
	out := make([]float32, elemCount)
	copy(out, unsafe.Slice((*float32)(cPtrOf(dataPtr)), elemCount))
	return out, nil
}
