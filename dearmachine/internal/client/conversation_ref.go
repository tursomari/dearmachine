package client

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"fmt"
	"strings"
)

const (
	conversationReferencePrefix   = "DM1-"
	legacyConversationRefBytes    = 16
	conversationReferenceAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	shortConversationPayloadChars = 11
	conversationFooterRule        = "----"
	conversationFooterMotto       = "Magnifica Humanitas"
	conversationFooterQuoteLabel  = "quote"
)

var conversationReferenceEncoding = base32.NewEncoding(conversationReferenceAlphabet).WithPadding(base32.NoPadding)

func newConversationReference() string {
	var entropy [shortConversationPayloadChars]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		panic(fmt.Sprintf("generate conversation reference: %v", err))
	}
	payload := make([]byte, len(entropy))
	for index, value := range entropy {
		payload[index] = conversationReferenceAlphabet[value&31]
	}
	lower := strings.ToLower(string(payload))
	return "dm1-" + lower[:5] + "-" + lower[5:]
}

func conversationReferenceChecksum(payload string) byte {
	sum := sha256.Sum256([]byte(conversationReferencePrefix + payload))
	return conversationReferenceAlphabet[int(sum[0]&31)]
}

func validConversationReference(reference string) bool {
	return canonicalConversationReference(reference) != ""
}

func canonicalConversationReference(reference string) string {
	reference = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(reference)), "-", "")
	if len(reference) != len("dm1")+shortConversationPayloadChars ||
		!strings.HasPrefix(reference, "dm1") {
		return ""
	}
	payload := strings.TrimPrefix(reference, "dm1")
	alphabet := strings.ToLower(conversationReferenceAlphabet)
	for _, character := range payload {
		if !strings.ContainsRune(alphabet, character) {
			return ""
		}
	}
	return "dm1-" + payload[:5] + "-" + payload[5:]
}

func isCanonicalConversationReference(reference string) bool {
	return reference != "" && canonicalConversationReference(reference) == reference
}

func canonicalInboundReference(reference string) string {
	reference = strings.ToUpper(strings.TrimSpace(reference))
	if strings.HasPrefix(reference, conversationReferencePrefix) {
		encoded := strings.TrimPrefix(reference, conversationReferencePrefix)
		if len(encoded) == 27 {
			payload, checksum := encoded[:len(encoded)-1], encoded[len(encoded)-1]
			decoded, err := conversationReferenceEncoding.DecodeString(payload)
			if err == nil && len(decoded) == legacyConversationRefBytes &&
				subtle.ConstantTimeByteEq(checksum, conversationReferenceChecksum(payload)) == 1 {
				short := strings.ToLower(payload[:shortConversationPayloadChars])
				return "dm1-" + short[:5] + "-" + short[5:]
			}
		}
	}
	return canonicalConversationReference(reference)
}

func shortConversationReference(reference string) string {
	return canonicalConversationReference(reference)
}

func conversationFooter(reference string) string {
	return conversationFooterRule + "\nDear Machine:\nsession " + shortConversationReference(reference)
}

func minimalConversationFooter(reference string) string {
	return "session " + shortConversationReference(reference)
}

func appendConversationFooter(text, reference string, quotes ...*MagnificaHumanitas) string {
	return appendConversationFooterMode(text, reference, false, quotes...)
}

func appendConversationFooterMode(text, reference string, minimal bool, quotes ...*MagnificaHumanitas) string {
	footer := conversationFooter(reference)
	if minimal {
		footer = minimalConversationFooter(reference)
	}
	var quote *MagnificaHumanitas
	if len(quotes) > 0 {
		quote = quotes[0]
	}
	if quoteText := magnificaHumanitasQuoteText(quote); quoteText != "" && !minimal {
		footer += "\n\n" + conversationFooterMotto + " " + conversationFooterQuoteLabel + ":\n\"" + quoteText + `"`
	}
	return strings.TrimRight(text, "\r\n") + "\n\n" + footer
}

func magnificaHumanitasQuoteText(quote *MagnificaHumanitas) string {
	if quote == nil {
		return ""
	}
	return strings.TrimSpace(quote.Quote)
}

func stripConversationFooters(body string) (string, []string) {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	lines := strings.Split(body, "\n")
	remove := make([]bool, len(lines))
	seen := make(map[string]struct{})
	validReferences := make(map[string]struct{})
	validReferenceLines := make([]int, 0)
	var references []string
	for index := 0; index+2 < len(lines); index++ {
		depth := emailQuoteDepth(lines[index])
		if footerLine(lines[index]) != conversationFooterRule ||
			emailQuoteDepth(lines[index+1]) != depth || footerLine(lines[index+1]) != "Dear Machine:" ||
			emailQuoteDepth(lines[index+2]) != depth {
			continue
		}
		session, found := strings.CutPrefix(footerLine(lines[index+2]), "session ")
		if !found || !isCanonicalConversationReference(session) {
			continue
		}
		reference := session
		end := index + 3
		if end+2 < len(lines) && footerLine(lines[end]) == "" &&
			emailQuoteDepth(lines[end+1]) == depth && footerLine(lines[end+1]) == conversationFooterMotto+" "+conversationFooterQuoteLabel+":" &&
			emailQuoteDepth(lines[end+2]) == depth && conversationFooterQuotedText(footerLine(lines[end+2])) {
			end += 3
		}
		for candidate := index; candidate < end; candidate++ {
			remove[candidate] = true
		}
		validReferences[reference] = struct{}{}
		validReferenceLines = append(validReferenceLines, index+2)
		if _, exists := seen[reference]; !exists {
			seen[reference] = struct{}{}
			references = append(references, reference)
		}
		index = end - 1
	}
	for index := range lines {
		if remove[index] {
			continue
		}
		if index > 0 && emailQuoteDepth(lines[index-1]) == emailQuoteDepth(lines[index]) &&
			strings.TrimSpace(footerLine(lines[index-1])) != "" {
			continue
		}
		session, found := strings.CutPrefix(footerLine(lines[index]), "session ")
		if !found || !isCanonicalConversationReference(session) {
			continue
		}
		remove[index] = true
		validReferences[session] = struct{}{}
		validReferenceLines = append(validReferenceLines, index)
		if _, exists := seen[session]; !exists {
			seen[session] = struct{}{}
			references = append(references, session)
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

func footerLine(line string) string {
	return stripEmailQuotePrefix(line)
}

func conversationFooterQuotedText(line string) bool {
	return len(line) > 2 && strings.HasPrefix(line, `"`) && strings.HasSuffix(line, `"`)
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

// stripReplyHistory keeps the newly authored, top-posted reply while removing
// a recognized mail-client history block. It intentionally does not remove
// free-standing ">" quotations or forwarded messages.
func stripReplyHistory(body string) string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	lines := strings.Split(body, "\n")
	for index := range lines {
		boundary := -1
		line := strings.TrimSpace(lines[index])
		if strings.EqualFold(line, "-----Original Message-----") {
			boundary = index
		} else if replyAttributionEnd(lines, index) >= index {
			boundary = index
		} else if outlookReplyHeader(lines, index) {
			boundary = index
			if index > 0 && mailHeaderSeparator(lines[index-1]) {
				boundary = index - 1
			}
		}
		if boundary < 0 {
			continue
		}
		contribution := strings.TrimSpace(strings.Join(lines[:boundary], "\n"))
		if contribution != "" {
			return contribution
		}
	}
	return strings.TrimSpace(body)
}

func replyAttributionEnd(lines []string, start int) int {
	first := strings.TrimSpace(lines[start])
	if !strings.HasPrefix(strings.ToLower(first), "on ") {
		return -1
	}
	combined := first
	for end := start; end < len(lines) && end < start+3; end++ {
		if end > start {
			part := strings.TrimSpace(lines[end])
			if part == "" {
				break
			}
			combined += " " + part
		}
		lower := strings.ToLower(combined)
		if strings.HasSuffix(lower, " wrote:") && strings.Contains(combined, "@") {
			return end
		}
	}
	return -1
}

func outlookReplyHeader(lines []string, start int) bool {
	from := strings.TrimSpace(lines[start])
	if !strings.HasPrefix(strings.ToLower(from), "from:") || !strings.Contains(from, "@") {
		return false
	}
	found := map[string]bool{}
	for index := start + 1; index < len(lines) && index < start+8; index++ {
		line := strings.ToLower(strings.TrimSpace(lines[index]))
		if line == "" {
			continue
		}
		for _, field := range []string{"sent:", "date:", "to:", "subject:"} {
			if strings.HasPrefix(line, field) {
				found[field] = true
			}
		}
	}
	return (found["sent:"] || found["date:"]) && found["to:"] && found["subject:"]
}

func mailHeaderSeparator(line string) bool {
	line = strings.TrimSpace(line)
	if len(line) < 10 {
		return false
	}
	for _, character := range line {
		if character != '_' && character != '-' {
			return false
		}
	}
	return true
}

func prepareInboundMessage(message Message) (Message, string) {
	if message.RawBody == "" {
		message.RawBody = message.Body
	}
	_, historyReferences := stripConversationFooters(message.Body)
	clean, bodyReferences := stripConversationFooters(stripReplyHistory(message.Body))
	message.Body = clean
	message.ConversationReferences = mergeConversationReferences(
		message.ConversationReferences,
		historyReferences,
		bodyReferences,
	)
	if len(message.ConversationReferences) == 1 {
		return message, message.ConversationReferences[0]
	}
	return message, ""
}
