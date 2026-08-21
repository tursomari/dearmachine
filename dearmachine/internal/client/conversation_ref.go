package client

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"fmt"
	"regexp"
	"strings"
)

const (
	conversationReferencePrefix   = "DM1-"
	conversationReferenceBytes    = 16
	conversationReferenceAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	shortConversationPayloadChars = 11
	conversationFooterMotto       = "Magnifica Humanitas"
)

var (
	conversationReferenceEncoding = base32.NewEncoding(conversationReferenceAlphabet).WithPadding(base32.NoPadding)
	conversationReferenceLine     = regexp.MustCompile(`(?i)^Dear\s*Machine\s*[-\x{2013}\x{2014}]\s*Ref:\s*([A-Z0-9-]+)\s*$`)
	conversationSessionLine       = regexp.MustCompile(`(?i)^session\s*:\s*([A-Z0-9-]+)\s*$`)
	conversationFooterNameLine    = regexp.MustCompile(`(?i)^Dear\s*Machine\s*$`)
)

func newConversationReference() string {
	var entropy [conversationReferenceBytes]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		panic(fmt.Sprintf("generate conversation reference: %v", err))
	}
	payload := conversationReferenceEncoding.EncodeToString(entropy[:])
	return conversationReferencePrefix + payload + string(conversationReferenceChecksum(payload))
}

func conversationReferenceChecksum(payload string) byte {
	sum := sha256.Sum256([]byte(conversationReferencePrefix + payload))
	return conversationReferenceAlphabet[int(sum[0]&31)]
}

func validConversationReference(reference string) bool {
	reference = strings.ToUpper(strings.TrimSpace(reference))
	if !strings.HasPrefix(reference, conversationReferencePrefix) {
		return false
	}
	encoded := strings.TrimPrefix(reference, conversationReferencePrefix)
	if len(encoded) != 27 {
		return false
	}
	payload, checksum := encoded[:len(encoded)-1], encoded[len(encoded)-1]
	decoded, err := conversationReferenceEncoding.DecodeString(payload)
	if err != nil || len(decoded) != conversationReferenceBytes {
		return false
	}
	return subtle.ConstantTimeByteEq(checksum, conversationReferenceChecksum(payload)) == 1
}

func canonicalConversationReference(reference string) string {
	reference = strings.ToUpper(strings.TrimSpace(reference))
	if !validConversationReference(reference) {
		return ""
	}
	return reference
}

func isCanonicalConversationReference(reference string) bool {
	return reference != "" && canonicalConversationReference(reference) == reference
}

func canonicalShortConversationReference(reference string) string {
	reference = strings.ToUpper(strings.TrimSpace(reference))
	if !strings.HasPrefix(reference, conversationReferencePrefix) {
		return ""
	}
	payload := strings.ReplaceAll(strings.TrimPrefix(reference, conversationReferencePrefix), "-", "")
	if len(payload) != shortConversationPayloadChars {
		return ""
	}
	for _, character := range payload {
		if !strings.ContainsRune(conversationReferenceAlphabet, character) {
			return ""
		}
	}
	return conversationReferencePrefix + payload
}

func canonicalInboundReference(reference string) string {
	if full := canonicalConversationReference(reference); full != "" {
		return full
	}
	return canonicalShortConversationReference(reference)
}

func shortConversationReference(reference string) string {
	reference = canonicalConversationReference(reference)
	if reference == "" {
		return ""
	}
	payload := strings.TrimPrefix(reference, conversationReferencePrefix)
	short := conversationReferencePrefix + payload[:5] + "-" + payload[5:shortConversationPayloadChars]
	return strings.ToLower(short)
}

func conversationFooter(reference string) string {
	return "--\nDear Machine\nsession: " + shortConversationReference(reference) + "\n" + conversationFooterMotto
}

func appendConversationFooter(text, reference string) string {
	return strings.TrimRight(text, "\r\n") + "\n\n" + conversationFooter(reference)
}

func stripConversationFooters(body string) (string, []string) {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	lines := strings.Split(body, "\n")
	remove := make([]bool, len(lines))
	seen := make(map[string]struct{})
	validReferences := make(map[string]struct{})
	validReferenceLines := make([]int, 0)
	var references []string
	for index, line := range lines {
		content := strings.TrimSpace(stripEmailQuotePrefix(line))
		match := conversationReferenceLine.FindStringSubmatch(content)
		isSessionLine := false
		if len(match) != 2 {
			match = conversationSessionLine.FindStringSubmatch(content)
			isSessionLine = len(match) == 2
		}
		if len(match) != 2 {
			continue
		}
		remove[index] = true
		previous := index - 1
		for previous >= 0 && strings.TrimSpace(stripEmailQuotePrefix(lines[previous])) == "" {
			previous--
		}
		if isSessionLine {
			if previous >= 0 && conversationFooterNameLine.MatchString(
				strings.TrimSpace(stripEmailQuotePrefix(lines[previous])),
			) {
				for candidate := previous; candidate < index; candidate++ {
					remove[candidate] = true
				}
				separator := previous - 1
				for separator >= 0 && strings.TrimSpace(stripEmailQuotePrefix(lines[separator])) == "" {
					separator--
				}
				if separator >= 0 && strings.TrimSpace(stripEmailQuotePrefix(lines[separator])) == "--" {
					for candidate := separator; candidate < previous; candidate++ {
						remove[candidate] = true
					}
				}
			}
		} else if previous >= 0 && strings.TrimSpace(stripEmailQuotePrefix(lines[previous])) == "--" {
			for candidate := previous; candidate < index; candidate++ {
				remove[candidate] = true
			}
		}
		next := index + 1
		for next < len(lines) && strings.TrimSpace(stripEmailQuotePrefix(lines[next])) == "" {
			next++
		}
		if next < len(lines) && strings.EqualFold(
			strings.TrimSpace(stripEmailQuotePrefix(lines[next])),
			conversationFooterMotto,
		) {
			for candidate := index + 1; candidate <= next; candidate++ {
				remove[candidate] = true
			}
		}
		reference := canonicalInboundReference(match[1])
		if reference == "" {
			continue
		}
		validReferences[reference] = struct{}{}
		validReferenceLines = append(validReferenceLines, index)
		if _, exists := seen[reference]; !exists {
			seen[reference] = struct{}{}
			references = append(references, reference)
		}
	}
	if len(validReferences) == 1 && hasUnquotedContribution(lines, remove) {
		for _, index := range validReferenceLines {
			if emailQuoteDepth(lines[index]) == 0 {
				continue
			}
			start, end := quotedBlockBounds(lines, index)
			for candidate := start; candidate < end; candidate++ {
				remove[candidate] = true
			}
		}
	}
	kept := make([]string, 0, len(lines))
	for index, line := range lines {
		if !remove[index] {
			kept = append(kept, line)
		}
	}
	return strings.TrimSpace(strings.Join(kept, "\n")), references
}

func hasUnquotedContribution(lines []string, remove []bool) bool {
	for index, line := range lines {
		if remove[index] || strings.TrimSpace(line) == "" || emailQuoteDepth(line) > 0 {
			continue
		}
		return true
	}
	return false
}

func quotedBlockBounds(lines []string, referenceLine int) (int, int) {
	start := referenceLine
	for start > 0 {
		candidate := lines[start-1]
		if strings.TrimSpace(candidate) != "" && emailQuoteDepth(candidate) == 0 {
			break
		}
		start--
	}
	end := referenceLine + 1
	for end < len(lines) {
		candidate := lines[end]
		if strings.TrimSpace(candidate) != "" && emailQuoteDepth(candidate) == 0 {
			break
		}
		end++
	}
	return start, end
}

func emailQuoteDepth(line string) int {
	line = strings.TrimLeft(line, " \t")
	depth := 0
	for strings.HasPrefix(line, ">") {
		depth++
		line = strings.TrimLeft(strings.TrimPrefix(line, ">"), " \t")
	}
	return depth
}

func stripEmailQuotePrefix(line string) string {
	for depth := emailQuoteDepth(line); depth > 0; depth-- {
		line = strings.TrimLeft(line, " \t")
		line = strings.TrimLeft(strings.TrimPrefix(line, ">"), " \t")
	}
	return line
}

func mergeConversationReferences(groups ...[]string) []string {
	seen := make(map[string]struct{})
	var merged []string
	for _, group := range groups {
		for _, candidate := range group {
			reference := canonicalInboundReference(candidate)
			if reference == "" {
				continue
			}
			if _, exists := seen[reference]; exists {
				continue
			}
			seen[reference] = struct{}{}
			merged = append(merged, reference)
		}
	}
	return merged
}

func conversationReferencesInBodies(bodies ...string) []string {
	var references []string
	for _, body := range bodies {
		_, found := stripConversationFooters(body)
		references = mergeConversationReferences(references, found)
	}
	return references
}

func prepareInboundMessage(message Message) (Message, string) {
	clean, bodyReferences := stripConversationFooters(message.Body)
	message.Body = clean
	message.ConversationReferences = mergeConversationReferences(
		message.ConversationReferences,
		bodyReferences,
	)
	if len(message.ConversationReferences) == 1 {
		return message, message.ConversationReferences[0]
	}
	return message, ""
}
