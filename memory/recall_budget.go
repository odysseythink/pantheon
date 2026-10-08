package memory

// Token / char budget allocator (D45-A static char proxy). Mirrors
// pipeline/recall/budget.py.
//
// Charge model: CJK ≈ 2 chars per token, ASCII/Latin ≈ 4 chars per token.
// The real LLM tokenizer will disagree by ±10-20%, but on the recall path
// we only need to decide which atoms fit into the budget.

const (
	CharsPerTokenCJK  = 2
	CharsPerTokenASCII = 4
)

const cjkStart, cjkEnd = 0x4E00, 0x9FFF

// EstimateTokens estimates prompt-token count using the static char proxy.
// Mixed-language strings are handled by summing ceil(cjk/2) + ceil(other/4).
// Returns >= 0; empty input returns 0.
func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	cjk, other := 0, 0
	for _, ch := range text {
		if ch >= cjkStart && ch <= cjkEnd {
			cjk++
		} else {
			other++
		}
	}
	// Round up each bucket so we don't under-estimate.
	cjkTokens := (cjk + CharsPerTokenCJK - 1) / CharsPerTokenCJK
	otherTokens := (other + CharsPerTokenASCII - 1) / CharsPerTokenASCII
	return cjkTokens + otherTokens
}

// BudgetBucket is which budget slice a snippet draws from. "raw" shares the
// "atom" cap (no separate slice). "quote" and "page_headline" are reserved.
type BudgetBucket string

const (
	BudgetBucketAtom        BudgetBucket = "atom"
	BudgetBucketQuote       BudgetBucket = "quote"
	BudgetBucketPageHeadline BudgetBucket = "page_headline"
	BudgetBucketRaw         BudgetBucket = "raw"
)

// DefaultTotalTokens is the soft default budget. Set conservatively because
// we'd rather under-recall than blow the host's prompt window.
const DefaultTotalTokens = 1500

// DefaultShares is the fraction of total tokens each bucket may consume.
// atom and raw deliberately share the 70% slice — raw is a fallback used
// only when atom comes back light, never alongside competing atom hits.
var DefaultShares = map[BudgetBucket]float64{
	BudgetBucketAtom:         0.70,
	BudgetBucketQuote:        0.20,
	BudgetBucketPageHeadline: 0.10,
	BudgetBucketRaw:          0.70,
}

// BudgetState is the live token-tracking state for a single recall call.
type BudgetState struct {
	totalTokens int
	bucketCaps  map[BudgetBucket]int
	BucketUsed  map[BudgetBucket]int
}

// TotalUsed returns the effective total consumption. raw and atom share a
// cap, so summing both would double-count — we take the max instead.
func (bs *BudgetState) TotalUsed() int {
	atomOrRaw := bs.BucketUsed[BudgetBucketAtom]
	if raw := bs.BucketUsed[BudgetBucketRaw]; raw > atomOrRaw {
		atomOrRaw = raw
	}
	return atomOrRaw + bs.BucketUsed[BudgetBucketQuote] + bs.BucketUsed[BudgetBucketPageHeadline]
}

// TryCharge attempts to consume tokens from bucket. Returns true and
// mutates state on success; returns false and leaves state unchanged if the
// charge would breach either the bucket cap or the total budget. A non-nil
// error marks a programming error (negative tokens / unknown bucket) that
// the caller must propagate, mirroring the Python ValueError.
func (bs *BudgetState) TryCharge(bucket BudgetBucket, tokens int) (bool, error) {
	if tokens < 0 {
		return false, fatalErrf("tokens must be non-negative, got %d", tokens)
	}
	if tokens == 0 {
		return true, nil
	}
	cap, ok := bs.bucketCaps[bucket]
	if !ok {
		return false, fatalErrf("unknown budget bucket: %q", bucket)
	}
	used := bs.BucketUsed[bucket]
	if used+tokens > cap {
		return false, nil
	}
	// raw / atom share — also enforce total.
	if bs.TotalUsed()+tokens > bs.totalTokens {
		return false, nil
	}
	bs.BucketUsed[bucket] = used + tokens
	return true, nil
}

// MakeBudgetState builds a fresh BudgetState for one recall call. shares
// lets callers override the default 70/20/10 split; values need not sum
// to 1.0 — buckets are independent caps sharing the same total.
func MakeBudgetState(totalTokens int, shares map[BudgetBucket]float64) *BudgetState {
	if totalTokens <= 0 {
		totalTokens = DefaultTotalTokens
	}
	useShares := DefaultShares
	if shares != nil {
		useShares = shares
	}
	caps := make(map[BudgetBucket]int, len(useShares))
	for b, frac := range useShares {
		caps[b] = int(float64(totalTokens) * frac)
	}
	return &BudgetState{
		totalTokens: totalTokens,
		bucketCaps:  caps,
		BucketUsed:  map[BudgetBucket]int{},
	}
}
