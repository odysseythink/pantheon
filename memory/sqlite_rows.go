package memory

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Row scanners mirroring the Python backend's _row_to_* converters.
//
// Every query selects columns in DDL order (SELECT * or table.* plus the
// node queries' extra atom_content projection), so each scanner below pins
// an explicit column order derived from the DDL in sqlite_backend.go.

type rowScanner interface{ Scan(dest ...any) error }

// JSON helpers matching Python json.dumps(..., ensure_ascii=False) /
// json.loads with the backend's "empty → empty container" guards.

func marshalJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func mustMarshalJSON(v any) string {
	s, err := marshalJSON(v)
	if err != nil {
		// map[string]any / []string / []any can only fail on unsupported
		// types; there is nothing sensible to do but panic, matching the
		// Python backend where json.dumps of the same payloads would raise.
		panic(fmt.Sprintf("memory: json encode: %v", err))
	}
	return s
}

func unmarshalObject(raw sql.NullString) (map[string]any, error) {
	if !raw.Valid || raw.String == "" {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw.String), &m); err != nil {
		return nil, fmt.Errorf("memory: json decode object: %w", err)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

func unmarshalList(raw sql.NullString) ([]string, error) {
	if !raw.Valid || raw.String == "" {
		return []string{}, nil
	}
	var l []string
	if err := json.Unmarshal([]byte(raw.String), &l); err != nil {
		return nil, fmt.Errorf("memory: json decode list: %w", err)
	}
	if l == nil {
		l = []string{}
	}
	return l, nil
}

func unmarshalAny(raw sql.NullString) (any, error) {
	if !raw.Valid || raw.String == "" {
		return nil, nil
	}
	var v any
	if err := json.Unmarshal([]byte(raw.String), &v); err != nil {
		return nil, fmt.Errorf("memory: json decode: %w", err)
	}
	return v, nil
}

func parseTime(s string) (time.Time, error) {
	return ParseDatetimeUTC(s)
}

func parseTimePtr(raw sql.NullString) (*time.Time, error) {
	if !raw.Valid || raw.String == "" {
		return nil, nil
	}
	t, err := ParseDatetimeUTC(raw.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func strPtr(raw sql.NullString) *string {
	if !raw.Valid {
		return nil
	}
	s := raw.String
	return &s
}

// ---------------------------------------------------------------------------
// memory_nodes — SELECT n.*, a.assertion AS atom_content (11 columns)
// ---------------------------------------------------------------------------

const nodeSelectColumns = `n.id, n.parent_id, n.level, n.atom_id, n.content, n.topic, n.conversation_id, n.created_at, n.updated_at, n.metadata, a.assertion AS atom_content`

func scanNode(r rowScanner) (*MemoryNode, error) {
	var (
		id, level, content, createdAt, updatedAt string
		parentID, atomID, topic, conversationID  sql.NullString
		metadata, atomContent                    sql.NullString
	)
	if err := r.Scan(&id, &parentID, &level, &atomID, &content, &topic, &conversationID,
		&createdAt, &updatedAt, &metadata, &atomContent); err != nil {
		return nil, err
	}
	meta, err := unmarshalObject(metadata)
	if err != nil {
		return nil, err
	}
	created, err := parseTime(createdAt)
	if err != nil {
		return nil, err
	}
	updated, err := parseTime(updatedAt)
	if err != nil {
		return nil, err
	}
	// A leaf does not own a second copy of fact text: project the AtomCard
	// assertion into Content when reading the node.
	if atomID.Valid && atomContent.Valid {
		content = atomContent.String
	}
	return &MemoryNode{
		ID:             id,
		ParentID:       strPtr(parentID),
		Level:          NodeLevel(level),
		Content:        content,
		Topic:          strPtr(topic),
		ConversationID: strPtr(conversationID),
		CreatedAt:      created,
		UpdatedAt:      updated,
		Metadata:       meta,
		AtomID:         strPtr(atomID),
	}, nil
}

// ---------------------------------------------------------------------------
// raw_events
// ---------------------------------------------------------------------------

func scanRaw(r rowScanner) (*RawEvent, error) {
	var (
		id, host, timestamp, eventType, content string
		sessionID, threadID, user               sql.NullString
		payload                                 sql.NullString
	)
	if err := r.Scan(&id, &host, &sessionID, &threadID, &user, &timestamp,
		&eventType, &content, &payload); err != nil {
		return nil, err
	}
	p, err := unmarshalObject(payload)
	if err != nil {
		return nil, err
	}
	ts, err := parseTime(timestamp)
	if err != nil {
		return nil, err
	}
	return &RawEvent{
		ID:        id,
		Host:      host,
		SessionID: strPtr(sessionID),
		ThreadID:  strPtr(threadID),
		User:      strPtr(user),
		Timestamp: ts,
		EventType: RawEventType(eventType),
		Content:   content,
		Payload:   p,
	}, nil
}

// ---------------------------------------------------------------------------
// candidates
// ---------------------------------------------------------------------------

func scanCandidate(r rowScanner) (*Candidate, error) {
	var (
		id, candidateType, status, title, assertion, verbatimQuote, quoteEventID string
		subjectName, subjectEntityType, confidence, importance                   string
		recommendedAction, promotionReason, extractorVersion, createdAt          string
		rawEventIDs, payload                                                     sql.NullString
		targetEntityID, decidedAt, decidedBy, sessionID                          sql.NullString
	)
	if err := r.Scan(&id, &rawEventIDs, &candidateType, &status, &title,
		&assertion, &verbatimQuote, &quoteEventID,
		&subjectName, &subjectEntityType, &targetEntityID,
		&confidence, &importance, &recommendedAction,
		&promotionReason, &extractorVersion,
		&createdAt, &decidedAt, &decidedBy, &sessionID, &payload); err != nil {
		return nil, err
	}
	eventIDs, err := unmarshalList(rawEventIDs)
	if err != nil {
		return nil, err
	}
	pl, err := unmarshalObject(payload)
	if err != nil {
		return nil, err
	}
	created, err := parseTime(createdAt)
	if err != nil {
		return nil, err
	}
	decided, err := parseTimePtr(decidedAt)
	if err != nil {
		return nil, err
	}
	return &Candidate{
		ID:                id,
		RawEventIDs:       eventIDs,
		CandidateType:     CandidateType(candidateType),
		Status:            CandidateStatus(status),
		Title:             title,
		Assertion:         assertion,
		VerbatimQuote:     verbatimQuote,
		QuoteEventID:      quoteEventID,
		SubjectName:       subjectName,
		SubjectEntityType: EntityType(subjectEntityType),
		TargetEntityID:    strPtr(targetEntityID),
		Confidence:        ConfidenceLevel(confidence),
		Importance:        ImportanceLevel(importance),
		RecommendedAction: RecommendedAction(recommendedAction),
		PromotionReason:   promotionReason,
		ExtractorVersion:  extractorVersion,
		CreatedAt:         created,
		DecidedAt:         decided,
		DecidedBy:         (*DecidedBy)(strPtr(decidedBy)),
		SessionID:         strPtr(sessionID),
		Payload:           pl,
	}, nil
}

// ---------------------------------------------------------------------------
// atoms
// ---------------------------------------------------------------------------

func scanAtom(r rowScanner) (*AtomCard, error) {
	var (
		id, entityID, candidateID, assertion, verbatimQuote, quoteEventID string
		occurredAt, confidence, importance, createdAt                     string
		rawEventIDs, searchTerms                                          sql.NullString
		supersededBy, deprecatedAt                                        sql.NullString
	)
	if err := r.Scan(&id, &entityID, &candidateID, &rawEventIDs,
		&assertion, &verbatimQuote, &quoteEventID, &searchTerms,
		&occurredAt, &confidence, &importance,
		&supersededBy, &deprecatedAt, &createdAt); err != nil {
		return nil, err
	}
	eventIDs, err := unmarshalList(rawEventIDs)
	if err != nil {
		return nil, err
	}
	terms, err := unmarshalList(searchTerms)
	if err != nil {
		return nil, err
	}
	occurred, err := parseTime(occurredAt)
	if err != nil {
		return nil, err
	}
	deprecated, err := parseTimePtr(deprecatedAt)
	if err != nil {
		return nil, err
	}
	created, err := parseTime(createdAt)
	if err != nil {
		return nil, err
	}
	return &AtomCard{
		ID:            id,
		EntityID:      entityID,
		CandidateID:   candidateID,
		RawEventIDs:   eventIDs,
		Assertion:     assertion,
		VerbatimQuote: verbatimQuote,
		QuoteEventID:  quoteEventID,
		SearchTerms:   terms,
		OccurredAt:    occurred,
		Confidence:    ConfidenceLevel(confidence),
		Importance:    ImportanceLevel(importance),
		CreatedAt:     created,
		SupersededBy:  strPtr(supersededBy),
		DeprecatedAt:  deprecated,
	}, nil
}

// ---------------------------------------------------------------------------
// entities
// ---------------------------------------------------------------------------

func scanEntity(r rowScanner) (*Entity, error) {
	var (
		id, entityType, canonicalName, createdAt string
		aliases                                  sql.NullString
		atomCount                                int64
		lastPromotedAt                           sql.NullString
	)
	if err := r.Scan(&id, &entityType, &canonicalName, &aliases,
		&atomCount, &lastPromotedAt, &createdAt); err != nil {
		return nil, err
	}
	aliasList, err := unmarshalList(aliases)
	if err != nil {
		return nil, err
	}
	created, err := parseTime(createdAt)
	if err != nil {
		return nil, err
	}
	promoted, err := parseTimePtr(lastPromotedAt)
	if err != nil {
		return nil, err
	}
	return &Entity{
		ID:             id,
		EntityType:     EntityType(entityType),
		CanonicalName:  canonicalName,
		Aliases:        aliasList,
		AtomCount:      int(atomCount),
		CreatedAt:      created,
		LastPromotedAt: promoted,
	}, nil
}

func scanAlias(r rowScanner) (*Alias, error) {
	var alias, entityID, entityType, createdBy, createdAt string
	if err := r.Scan(&alias, &entityID, &entityType, &createdBy, &createdAt); err != nil {
		return nil, err
	}
	created, err := parseTime(createdAt)
	if err != nil {
		return nil, err
	}
	return &Alias{
		Alias:      alias,
		EntityID:   entityID,
		EntityType: EntityType(entityType),
		CreatedBy:  DecidedBy(createdBy),
		CreatedAt:  created,
	}, nil
}

// ---------------------------------------------------------------------------
// entity_pages
// ---------------------------------------------------------------------------

func scanEntityPage(r rowScanner) (*EntityPage, error) {
	var (
		id, entityID, createdAt, updatedAt string
		summaryMarkdown, headline          sql.NullString
		topics                             sql.NullString
		dirty                              int64
		regenAttemptCount, summaryVersion  int64
		lastRegenAt, lastUserEditAt        sql.NullString
	)
	if err := r.Scan(&id, &entityID, &summaryMarkdown, &headline, &topics,
		&dirty, &regenAttemptCount, &summaryVersion,
		&lastRegenAt, &lastUserEditAt, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	topicList, err := unmarshalList(topics)
	if err != nil {
		return nil, err
	}
	created, err := parseTime(createdAt)
	if err != nil {
		return nil, err
	}
	updated, err := parseTime(updatedAt)
	if err != nil {
		return nil, err
	}
	lastRegen, err := parseTimePtr(lastRegenAt)
	if err != nil {
		return nil, err
	}
	lastEdit, err := parseTimePtr(lastUserEditAt)
	if err != nil {
		return nil, err
	}
	return &EntityPage{
		ID:                id,
		EntityID:          entityID,
		SummaryMarkdown:   nullDefault(summaryMarkdown, ""),
		Headline:          nullDefault(headline, ""),
		Topics:            topicList,
		Dirty:             dirty != 0,
		RegenAttemptCount: int(regenAttemptCount),
		SummaryVersion:    int(summaryVersion),
		CreatedAt:         created,
		UpdatedAt:         updated,
		LastRegenAt:       lastRegen,
		LastUserEditAt:    lastEdit,
	}, nil
}

func nullDefault(raw sql.NullString, def string) string {
	if !raw.Valid {
		return def
	}
	return raw.String
}

// ---------------------------------------------------------------------------
// journal
// ---------------------------------------------------------------------------

func scanJournal(r rowScanner) (*JournalEntry, error) {
	var (
		id, timestamp, action, actor       string
		noteRaw                            sql.NullString
		targetEntityID, targetAtomID       sql.NullString
		targetCandidateID                  sql.NullString
		before, after                      sql.NullString
	)
	if err := r.Scan(&id, &timestamp, &action, &actor,
		&targetEntityID, &targetAtomID, &targetCandidateID,
		&before, &after, &noteRaw); err != nil {
		return nil, err
	}
	ts, err := parseTime(timestamp)
	if err != nil {
		return nil, err
	}
	beforeVal, err := unmarshalAny(before)
	if err != nil {
		return nil, err
	}
	afterVal, err := unmarshalAny(after)
	if err != nil {
		return nil, err
	}
	beforeMap, _ := beforeVal.(map[string]any)
	afterMap, _ := afterVal.(map[string]any)
	return &JournalEntry{
		ID:                id,
		Timestamp:         ts,
		Action:            JournalAction(action),
		Actor:             DecidedBy(actor),
		TargetEntityID:    strPtr(targetEntityID),
		TargetAtomID:      strPtr(targetAtomID),
		TargetCandidateID: strPtr(targetCandidateID),
		Before:            beforeMap,
		After:             afterMap,
		Note:              nullDefault(noteRaw, ""),
	}, nil
}

// ---------------------------------------------------------------------------
// episodes
// ---------------------------------------------------------------------------

func scanEpisode(r rowScanner) (*Episode, error) {
	var (
		id, occurredAt, summary, verbatimQuote, quoteEventID string
		emotion, extractorVersion, createdAt                 string
		rawEventIDs, people, topics, digestIDs               sql.NullString
		intensity                                            int64
		sessionID                                            sql.NullString
	)
	if err := r.Scan(&id, &rawEventIDs, &occurredAt, &summary, &verbatimQuote, &quoteEventID,
		&emotion, &intensity, &people, &topics, &extractorVersion, &sessionID,
		&digestIDs, &createdAt); err != nil {
		return nil, err
	}
	eventIDs, err := unmarshalList(rawEventIDs)
	if err != nil {
		return nil, err
	}
	peopleList, err := unmarshalList(people)
	if err != nil {
		return nil, err
	}
	topicList, err := unmarshalList(topics)
	if err != nil {
		return nil, err
	}
	digestList, err := unmarshalList(digestIDs)
	if err != nil {
		return nil, err
	}
	occurred, err := parseTime(occurredAt)
	if err != nil {
		return nil, err
	}
	created, err := parseTime(createdAt)
	if err != nil {
		return nil, err
	}
	return &Episode{
		ID:               id,
		RawEventIDs:      eventIDs,
		OccurredAt:       occurred,
		Summary:          summary,
		VerbatimQuote:    verbatimQuote,
		QuoteEventID:     quoteEventID,
		Emotion:          EpisodeEmotion(emotion),
		Intensity:        int(intensity),
		People:           peopleList,
		Topics:           topicList,
		ExtractorVersion: extractorVersion,
		CreatedAt:        created,
		SessionID:        strPtr(sessionID),
		DigestIDs:        digestList,
	}, nil
}

// ---------------------------------------------------------------------------
// digests
// ---------------------------------------------------------------------------

func scanDigest(r rowScanner) (*DigestRecord, error) {
	var (
		id, periodKind, periodKey, periodStart, periodEnd string
		llmVersion, createdAt, updatedAt                  string
		markdownRaw                                       sql.NullString
		episodeIDs                                        sql.NullString
	)
	if err := r.Scan(&id, &periodKind, &periodKey, &periodStart, &periodEnd,
		&markdownRaw, &episodeIDs, &llmVersion, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	episodes, err := unmarshalList(episodeIDs)
	if err != nil {
		return nil, err
	}
	start, err := parseTime(periodStart)
	if err != nil {
		return nil, err
	}
	end, err := parseTime(periodEnd)
	if err != nil {
		return nil, err
	}
	created, err := parseTime(createdAt)
	if err != nil {
		return nil, err
	}
	updated, err := parseTime(updatedAt)
	if err != nil {
		return nil, err
	}
	return &DigestRecord{
		ID:          id,
		PeriodKind:  DigestPeriod(periodKind),
		PeriodKey:   periodKey,
		PeriodStart: start,
		PeriodEnd:   end,
		Markdown:    nullDefault(markdownRaw, ""),
		EpisodeIDs:  episodes,
		LLMVersion:  llmVersion,
		CreatedAt:   created,
		UpdatedAt:   updated,
	}, nil
}

// ---------------------------------------------------------------------------
// thread_active_entities
// ---------------------------------------------------------------------------

func scanActiveEntity(r rowScanner) (*ActiveEntity, error) {
	var threadID, entityID, lastSeenAt, source string
	if err := r.Scan(&threadID, &entityID, &lastSeenAt, &source); err != nil {
		return nil, err
	}
	seen, err := parseTime(lastSeenAt)
	if err != nil {
		return nil, err
	}
	if source == "" {
		source = "recall_hit"
	}
	return &ActiveEntity{
		ThreadID:   threadID,
		EntityID:   entityID,
		LastSeenAt: seen,
		Source:     ActiveEntitySource(source),
	}, nil
}
