package memory

import (
	"database/sql/driver"
	"sync"

	"modernc.org/sqlite"
)

// Driver-side registration of the hm_cjk_seg SQL scalar function.
//
// Mirrors fts_text.register_fts_functions: the FTS-sync triggers wrap every
// indexed value expression in hm_cjk_seg(...), so the function MUST be
// registered on the driver before any connection touches a table with
// FTS-sync triggers. modernc.org/sqlite registers functions process-wide, so
// a single idempotent call covers every pooled connection.

var registerFTSFuncsOnce sync.Once

func registerFTSFunctions() {
	registerFTSFuncsOnce.Do(func() {
		sqlite.MustRegisterDeterministicScalarFunction(SQLFuncCJKSeg, 1,
			func(ctx *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
				if len(args) != 1 {
					return nil, nil
				}
				switch v := args[0].(type) {
				case nil:
					return nil, nil
				case string:
					return SegmentCJK(v), nil
				case []byte:
					return []byte(SegmentCJK(string(v))), nil
				default:
					return args[0], nil
				}
			})
	})
}
