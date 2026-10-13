package localonnx

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// This file ports the Hugging Face tokenizers behavior for the BERT family
// (BertNormalizer + BertPreTokenizer + WordPiece + TemplateProcessing), which
// is what fastembed runs through `tokenizers.Tokenizer.from_file` for every
// WordPiece catalog entry. fastembed then calls enable_truncation(max_length)
// before encoding, so the same truncation semantics apply here.

// tokenizerJSON is the subset of tokenizer.json this package understands.
type tokenizerJSON struct {
	Normalizer struct {
		Type               string `json:"type"`
		CleanText          bool   `json:"clean_text"`
		HandleChineseChars bool   `json:"handle_chinese_chars"`
		StripAccents       *bool  `json:"strip_accents"`
		Lowercase          bool   `json:"lowercase"`
	} `json:"normalizer"`
	PreTokenizer struct {
		Type string `json:"type"`
	} `json:"pre_tokenizer"`
	Model struct {
		Type                    string         `json:"type"`
		Vocab                   map[string]int `json:"vocab"`
		UnkToken                string         `json:"unk_token"`
		ContinuingSubwordPrefix string         `json:"continuing_subword_prefix"`
		MaxInputCharsPerWord    int            `json:"max_input_chars_per_word"`
	} `json:"model"`
	PostProcessor struct {
		Type   string `json:"type"`
		Single []struct {
			SpecialToken *struct {
				ID string `json:"id"`
			} `json:"SpecialToken"`
		} `json:"single"`
	} `json:"post_processor"`
	AddedTokens []struct {
		ID      int    `json:"id"`
		Content string `json:"content"`
		Special bool   `json:"special"`
	} `json:"added_tokens"`
}

// Tokenizer encodes text into WordPiece ids for one model directory.
type Tokenizer struct {
	vocab        map[string]int
	unkID        int
	clsID        int
	sepID        int
	maxLen       int
	maxCharsWord int
	cleanText    bool
	chineseChars bool
	stripAccents bool
	lowercase    bool
	prefix       string
	addedTokens  []addedToken // sorted by content length desc
	addedIDs     map[string]int
}

type addedToken struct {
	content string
	id      int
}

// LoadTokenizer reads tokenizer.json (+ tokenizer_config.json /
// config.json for the truncation length) from a model snapshot directory.
func LoadTokenizer(dir string) (*Tokenizer, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "tokenizer.json"))
	if err != nil {
		return nil, fmt.Errorf("read tokenizer.json: %w", err)
	}
	var tj tokenizerJSON
	if err := json.Unmarshal(raw, &tj); err != nil {
		return nil, fmt.Errorf("parse tokenizer.json: %w", err)
	}
	if tj.Normalizer.Type != "" && tj.Normalizer.Type != "BertNormalizer" {
		return nil, fmt.Errorf("unsupported normalizer %q (only the BERT family is implemented)", tj.Normalizer.Type)
	}
	if tj.PreTokenizer.Type != "" && tj.PreTokenizer.Type != "BertPreTokenizer" {
		return nil, fmt.Errorf("unsupported pre_tokenizer %q (only the BERT family is implemented)", tj.PreTokenizer.Type)
	}
	if tj.Model.Type != "WordPiece" {
		return nil, fmt.Errorf("unsupported tokenizer model %q (WordPiece only)", tj.Model.Type)
	}
	if len(tj.Model.Vocab) == 0 {
		return nil, fmt.Errorf("tokenizer.json has an empty vocab")
	}

	maxChars := tj.Model.MaxInputCharsPerWord
	if maxChars <= 0 {
		maxChars = 100
	}
	t := &Tokenizer{
		vocab:        make(map[string]int, len(tj.Model.Vocab)),
		unkID:        -1,
		maxLen:       512,
		maxCharsWord: maxChars,
		cleanText:    tj.Normalizer.CleanText,
		chineseChars: tj.Normalizer.HandleChineseChars,
		lowercase:    tj.Normalizer.Lowercase,
		prefix:       tj.Model.ContinuingSubwordPrefix,
		addedIDs:     map[string]int{},
	}
	// strip_accents: null follows lowercase (HF BertNormalizer behavior).
	if tj.Normalizer.StripAccents != nil {
		t.stripAccents = *tj.Normalizer.StripAccents
	} else {
		t.stripAccents = tj.Normalizer.Lowercase
	}
	for tok, id := range tj.Model.Vocab {
		t.vocab[tok] = id
	}
	if id, ok := t.vocab[tj.Model.UnkToken]; ok {
		t.unkID = id
	}
	for _, at := range tj.AddedTokens {
		if !at.Special || at.Content == "" {
			continue
		}
		id := at.ID
		if v, ok := t.vocab[at.Content]; ok {
			id = v
		}
		t.addedTokens = append(t.addedTokens, addedToken{content: at.Content, id: id})
		t.addedIDs[at.Content] = id
	}
	// Longest first so overlapping literals ([CLS] vs [UNK] prefixes) split
	// deterministically, matching HF AddedVocabulary priority.
	for i := 0; i < len(t.addedTokens); i++ {
		for j := i + 1; j < len(t.addedTokens); j++ {
			if len(t.addedTokens[j].content) > len(t.addedTokens[i].content) {
				t.addedTokens[i], t.addedTokens[j] = t.addedTokens[j], t.addedTokens[i]
			}
		}
	}
	// Special token ids for the [CLS] A [SEP] template.
	if id, ok := t.vocab["[CLS]"]; ok {
		t.clsID = id
	}
	if id, ok := t.vocab["[SEP]"]; ok {
		t.sepID = id
	}
	if t.unkID < 0 || t.clsID <= 0 || t.sepID <= 0 {
		return nil, fmt.Errorf("tokenizer.json vocab is missing [UNK]/[CLS]/[SEP]")
	}

	if ml, err := resolveMaxContext(dir); err == nil && ml > 0 {
		t.maxLen = ml
	}
	return t, nil
}

// resolveMaxContext ports _resolve_max_context: tokenizer_config.json's
// model_max_length wins, then config.json's max_position_embeddings.
func resolveMaxContext(dir string) (int, error) {
	if raw, err := os.ReadFile(filepath.Join(dir, "tokenizer_config.json")); err == nil {
		var cfg struct {
			ModelMaxLength any `json:"model_max_length"`
		}
		if json.Unmarshal(raw, &cfg) == nil {
			if v := anyToInt(cfg.ModelMaxLength); v > 0 {
				return v, nil
			}
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return 0, err
	}
	var cfg struct {
		MaxPositionEmbeddings any `json:"max_position_embeddings"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return 0, err
	}
	return anyToInt(cfg.MaxPositionEmbeddings), nil
}

func anyToInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return 0
	}
}

// Encode ports Tokenizer.encode with truncation enabled: it returns
// [CLS] tokens... [SEP] ids and the matching attention mask.
func (t *Tokenizer) Encode(text string) (ids []int, mask []int) {
	toks := t.tokenize(text)
	if len(toks) > t.maxLen-2 {
		toks = toks[:t.maxLen-2]
	}
	ids = make([]int, 0, len(toks)+2)
	mask = make([]int, 0, len(toks)+2)
	ids = append(ids, t.clsID)
	mask = append(mask, 1)
	for _, tok := range toks {
		ids = append(ids, t.vocab[tok])
		mask = append(mask, 1)
	}
	ids = append(ids, t.sepID)
	mask = append(mask, 1)
	return ids, mask
}

// MaxLen reports the truncation length used for encoding.
func (t *Tokenizer) MaxLen() int { return t.maxLen }

// tokenize runs the added-token split, normalization, pre-tokenization and
// WordPiece, returning token strings (ids are resolved in Encode).
func (t *Tokenizer) tokenize(text string) []string {
	var toks []string
	for _, seg := range t.splitAddedTokens(text) {
		if seg.added {
			toks = append(toks, seg.text) // literal special token, unmodified
			continue
		}
		normalized := t.normalize(seg.text)
		for _, word := range bertPreTokenize(normalized) {
			toks = append(toks, wordPiece(word, t.vocab, t.unkID, t.prefix, t.maxCharsWord)...)
		}
	}
	return toks
}

type textSeg struct {
	text  string
	added bool
}

// splitAddedTokens ports the AddedVocabulary pre-split: literal special
// tokens are cut out of the raw text before any normalization.
func (t *Tokenizer) splitAddedTokens(text string) []textSeg {
	if len(t.addedTokens) == 0 {
		return []textSeg{{text: text}}
	}
	var segs []textSeg
	for text != "" {
		bestIdx, bestTok := -1, -1
		for i, at := range t.addedTokens {
			if idx := strings.Index(text, at.content); idx >= 0 && (bestIdx < 0 || idx < bestIdx) {
				bestIdx, bestTok = idx, i
			}
		}
		if bestIdx < 0 {
			segs = append(segs, textSeg{text: text})
			break
		}
		if bestIdx > 0 {
			segs = append(segs, textSeg{text: text[:bestIdx]})
		}
		segs = append(segs, textSeg{text: t.addedTokens[bestTok].content, added: true})
		text = text[bestIdx+len(t.addedTokens[bestTok].content):]
	}
	return segs
}

// normalize ports BertNormalizer: clean_text, handle_chinese_chars,
// strip_accents, lowercase (in HF tokenizers' order).
func (t *Tokenizer) normalize(text string) string {
	if t.cleanText {
		text = cleanText(text)
	}
	if t.chineseChars {
		text = spaceAroundChinese(text)
	}
	if t.stripAccents {
		text = stripAccents(text)
	}
	if t.lowercase {
		text = strings.ToLower(text)
	}
	return text
}

// cleanText maps whitespace to ' ' and drops control characters.
func cleanText(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		switch {
		case unicode.IsSpace(r):
			b.WriteRune(' ')
		case unicode.IsControl(r):
			// dropped
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// spaceAroundChinese adds spaces around CJK ideographs (BERT's
// _is_chinese_char ranges — punctuation is NOT included here; the
// pre-tokenizer handles it).
func spaceAroundChinese(text string) string {
	var b strings.Builder
	b.Grow(len(text) + 16)
	for _, r := range text {
		if isChineseChar(r) {
			b.WriteRune(' ')
			b.WriteRune(r)
			b.WriteRune(' ')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isChineseChar(r rune) bool {
	switch {
	case r >= 0x4E00 && r <= 0x9FFF,
		r >= 0x3400 && r <= 0x4DBF,
		r >= 0x20000 && r <= 0x2A6DF,
		r >= 0x2A700 && r <= 0x2B73F,
		r >= 0x2B740 && r <= 0x2B81F,
		r >= 0x2B820 && r <= 0x2CEAF,
		r >= 0xF900 && r <= 0xFAFF,
		r >= 0x2F800 && r <= 0x2FA1F:
		return true
	}
	return false
}

// stripAccents NFD-decomposes and drops nonspacing marks.
func stripAccents(text string) string {
	if norm.NFD.String(text) == text && !hasMn(text) {
		return text
	}
	decomposed := norm.NFD.String(text)
	var b strings.Builder
	b.Grow(len(decomposed))
	for _, r := range decomposed {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func hasMn(text string) bool {
	for _, r := range text {
		if unicode.Is(unicode.Mn, r) {
			return true
		}
	}
	return false
}

// bertPreTokenize ports BertPreTokenizer: split on whitespace and isolate
// punctuation (BERT's _is_punctuation: the ASCII 33-126 non-alnum range plus
// Unicode category P*).
func bertPreTokenize(text string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range text {
		switch {
		case unicode.IsSpace(r):
			flush()
		case isPunct(r):
			flush()
			out = append(out, string(r))
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

func isPunct(r rune) bool {
	switch {
	case r >= '!' && r <= '/', r >= ':' && r <= '@',
		r >= '[' && r <= '`', r >= '{' && r <= '~':
		return true
	}
	return unicode.Is(unicode.P, r)
}

// wordPiece ports the WordPiece greedy longest-match-first algorithm:
// unknown in any position marks the whole word [UNK]; continuation pieces
// carry the "##" prefix; over-long words become [UNK] outright.
func wordPiece(word string, vocab map[string]int, unkID int, prefix string, maxChars int) []string {
	runes := []rune(word)
	if len(runes) > maxChars {
		return []string{"[UNK]"}
	}
	var out []string
	start := 0
	for start < len(runes) {
		end := len(runes)
		var cur string
		found := false
		for end > start {
			cand := string(runes[start:end])
			if start > 0 {
				cand = prefix + cand
			}
			if _, ok := vocab[cand]; ok {
				cur = cand
				found = true
				break
			}
			end--
		}
		if !found {
			return []string{"[UNK]"}
		}
		out = append(out, cur)
		start = end
	}
	return out
}
