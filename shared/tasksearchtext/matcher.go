package tasksearchtext

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/rivo/uniseg"
)

type LiteralCaseMode uint8

const (
	LiteralCaseInsensitive LiteralCaseMode = iota + 1
	LiteralCaseSensitive
)

var errEmptyLiteralQuery = errors.New("literal query has no matchable runes")

type LiteralMatcher struct {
	caseMode            LiteralCaseMode
	matchQuery          []rune
	candidateExpression string
	prefix              []int
}

type LiteralHit struct {
	Before         string
	Match          string
	After          string
	LeftTruncated  bool
	RightTruncated bool
}

func NewLiteralMatcher(query string, caseMode LiteralCaseMode) (LiteralMatcher, error) {
	if query == "" {
		return LiteralMatcher{}, errEmptyLiteralQuery
	}
	if caseMode != LiteralCaseInsensitive && caseMode != LiteralCaseSensitive {
		return LiteralMatcher{}, errors.New("literal case mode is invalid")
	}

	candidateQuery := normalizeInsensitiveRunes([]rune(query))
	if len(candidateQuery) == 0 {
		return LiteralMatcher{}, errEmptyLiteralQuery
	}
	matchQuery := candidateQuery
	if caseMode == LiteralCaseSensitive {
		matchQuery = []rune(query)
	}

	return LiteralMatcher{
		caseMode:            caseMode,
		matchQuery:          matchQuery,
		candidateExpression: serializeLiteralFTS5(candidateQuery),
		prefix:              kmpPrefix(matchQuery),
	}, nil
}

func (m LiteralMatcher) CandidateExpression() string {
	return m.candidateExpression
}

func (m LiteralMatcher) OccurrenceCount(source string) int {
	document := newLiteralDocument(source, m.caseMode)
	count := 0
	m.visitOccurrences(document, func(literalOccurrence) bool {
		count++
		return true
	})
	return count
}

func (m LiteralMatcher) NthHit(source string, ordinal int, contextClusters int) (LiteralHit, bool) {
	if ordinal < 1 {
		panic(fmt.Sprintf("literal matcher nth-hit ordinal must be positive: ordinal=%d", ordinal))
	}
	if contextClusters < 0 {
		panic(fmt.Sprintf("literal matcher context must be non-negative: context_clusters=%d", contextClusters))
	}
	document := newLiteralDocument(source, m.caseMode)
	seen := 0
	var selected literalOccurrence
	found := m.visitOccurrences(document, func(occurrence literalOccurrence) bool {
		seen++
		if seen != ordinal {
			return true
		}
		selected = occurrence
		return false
	})
	if found {
		return LiteralHit{}, false
	}
	return materializeLiteralHit(document, selected, contextClusters), true
}

func serializeLiteralFTS5(query []rune) string {
	var expression strings.Builder
	expression.Grow(len(query) + 2)
	expression.WriteByte('"')
	for _, character := range query {
		if character == '"' {
			expression.WriteByte('"')
		}
		expression.WriteRune(character)
	}
	expression.WriteByte('"')
	return expression.String()
}

func normalizeInsensitiveRunes(input []rune) []rune {
	normalized := make([]rune, 0, len(input))
	for _, character := range input {
		if normalizedCharacter := normalizeInsensitiveRune(character); normalizedCharacter != 0 {
			normalized = append(normalized, normalizedCharacter)
		}
	}
	return normalized
}

func normalizeInsensitiveRune(character rune) rune {
	index := sort.Search(len(insensitiveNormalizationMappings), func(index int) bool {
		return insensitiveNormalizationMappings[index].from >= character
	})
	if index < len(insensitiveNormalizationMappings) && insensitiveNormalizationMappings[index].from == character {
		return insensitiveNormalizationMappings[index].to
	}
	return character
}

func kmpPrefix(query []rune) []int {
	prefix := make([]int, len(query))
	for index := 1; index < len(query); index++ {
		matched := prefix[index-1]
		for matched > 0 && query[index] != query[matched] {
			matched = prefix[matched-1]
		}
		if query[index] == query[matched] {
			matched++
		}
		prefix[index] = matched
	}
	return prefix
}

type literalDocument struct {
	clusters []string
	stream   []literalStreamRune
}

type literalStreamRune struct {
	character rune
	cluster   int
}

type literalOccurrence struct {
	firstCluster int
	lastCluster  int
}

func newLiteralDocument(source string, caseMode LiteralCaseMode) literalDocument {
	document := literalDocument{}
	graphemes := uniseg.NewGraphemes(source)
	for graphemes.Next() {
		clusterIndex := len(document.clusters)
		cluster := graphemes.Str()
		document.clusters = append(document.clusters, cluster)
		for _, character := range cluster {
			if caseMode == LiteralCaseInsensitive {
				character = normalizeInsensitiveRune(character)
			}
			if character == 0 {
				continue
			}
			document.stream = append(document.stream, literalStreamRune{
				character: character,
				cluster:   clusterIndex,
			})
		}
	}
	return document
}

func (m LiteralMatcher) visitOccurrences(document literalDocument, visit func(literalOccurrence) bool) bool {
	matched := 0
	consumedCluster := -1
	for streamIndex, character := range document.stream {
		if character.cluster <= consumedCluster {
			continue
		}
		for matched > 0 && m.matchQuery[matched] != character.character {
			matched = m.prefix[matched-1]
		}
		if m.matchQuery[matched] == character.character {
			matched++
		}
		if matched != len(m.matchQuery) {
			continue
		}

		start := streamIndex - len(m.matchQuery) + 1
		occurrence := literalOccurrence{
			firstCluster: document.stream[start].cluster,
			lastCluster:  character.cluster,
		}
		if !visit(occurrence) {
			return false
		}
		matched = 0
		consumedCluster = occurrence.lastCluster
	}
	return true
}

func materializeLiteralHit(document literalDocument, occurrence literalOccurrence, contextClusters int) LiteralHit {
	beforeStart := occurrence.firstCluster - contextClusters
	if beforeStart < 0 {
		beforeStart = 0
	}
	afterEnd := occurrence.lastCluster + contextClusters + 1
	if afterEnd > len(document.clusters) {
		afterEnd = len(document.clusters)
	}
	return LiteralHit{
		Before:         strings.Join(document.clusters[beforeStart:occurrence.firstCluster], ""),
		Match:          strings.Join(document.clusters[occurrence.firstCluster:occurrence.lastCluster+1], ""),
		After:          strings.Join(document.clusters[occurrence.lastCluster+1:afterEnd], ""),
		LeftTruncated:  beforeStart > 0,
		RightTruncated: afterEnd < len(document.clusters),
	}
}
