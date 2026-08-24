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
	legacyConversationRefBytes    = 16
	conversationReferenceAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	shortConversationPayloadChars = 11
	conversationFooterRule        = "----"
	conversationFooterMotto       = "Magnifica Humanitas"
	conversationFooterQuoteLabel  = "quote"
)

var (
	conversationReferenceEncoding   = base32.NewEncoding(conversationReferenceAlphabet).WithPadding(base32.NoPadding)
	conversationReferenceLine       = regexp.MustCompile(`(?i)^Dear\s*Machine\s*[-\x{2013}\x{2014}]\s*Ref:\s*([A-Z0-9-]+)\s*$`)
	conversationSessionLine         = regexp.MustCompile(`(?i)^session\s*:?\s*([A-Z0-9-]+)\s*$`)
	conversationSessionIdentityLine = regexp.MustCompile(
		`(?i)^Dear\s*Machine\s*:\s*session\s*:?\s*([A-Z0-9-]+)\s*$`,
	)
	legacyConversationFooterLine = regexp.MustCompile(
		`(?i)^--\s*Dear\s*Machine\s*:\s*session\s*:?\s*([A-Z0-9-]+)(?:\s+(?:` + conversationFooterMotto + `\s+)?` + conversationFooterQuoteLabel + `\s*:\s*.*)?$`,
	)
	conversationFooterNameLine       = regexp.MustCompile(`(?i)^Dear\s*Machine\s*:?\s*$`)
	conversationFooterRuleLine       = regexp.MustCompile(`^-{2,7}$`)
	conversationFooterQuotedTextLine = regexp.MustCompile(`^".*"$`)
	conversationFooterQuoteLabelLine = regexp.MustCompile(
		`(?i)^` + conversationFooterMotto + `\s+` + conversationFooterQuoteLabel + `\s*:\s*$`,
	)
	conversationFooterQuoteLine = regexp.MustCompile(
		`(?i)^(?:` + conversationFooterMotto + `\s+)?` + conversationFooterQuoteLabel + `\s*:\s*.*$`,
	)
)

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

func appendConversationFooter(text, reference string, quotes ...*MagnificaHumanitas) string {
	footer := conversationFooter(reference)
	var quote *MagnificaHumanitas
	if len(quotes) > 0 {
		quote = quotes[0]
	}
	if quoteText := magnificaHumanitasQuoteText(quote); quoteText != "" {
		footer += "\n" + conversationFooterMotto + " " + conversationFooterQuoteLabel + ":\n\"" + quoteText + `"`
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
	for index, line := range lines {
		content := strings.TrimSpace(stripEmailQuotePrefix(line))
		if match := legacyConversationFooterLine.FindStringSubmatch(content); len(match) == 2 {
			reference := canonicalInboundReference(match[1])
			if reference == "" {
				continue
			}
			remove[index] = true
			validReferences[reference] = struct{}{}
			validReferenceLines = append(validReferenceLines, index)
			if _, exists := seen[reference]; !exists {
				seen[reference] = struct{}{}
				references = append(references, reference)
			}
			continue
		}
		match := conversationReferenceLine.FindStringSubmatch(content)
		isSessionLine := false
		combinedSessionLine := false
		if len(match) != 2 {
			match = conversationSessionLine.FindStringSubmatch(content)
			isSessionLine = len(match) == 2
		}
		if len(match) != 2 {
			match = conversationSessionIdentityLine.FindStringSubmatch(content)
			isSessionLine = len(match) == 2
			combinedSessionLine = isSessionLine
		}
		if len(match) != 2 {
			continue
		}
		reference := canonicalInboundReference(match[1])
		remove[index] = true
		previous := index - 1
		for previous >= 0 && strings.TrimSpace(stripEmailQuotePrefix(lines[previous])) == "" {
			previous--
		}
		validatedSessionFooter := false
		if combinedSessionLine {
			if previous >= 0 && conversationFooterRuleLine.MatchString(
				strings.TrimSpace(stripEmailQuotePrefix(lines[previous])),
			) {
				validatedSessionFooter = reference != ""
				for candidate := previous; candidate < index; candidate++ {
					remove[candidate] = true
				}
			}
		} else if isSessionLine {
			if previous >= 0 && conversationFooterNameLine.MatchString(
				strings.TrimSpace(stripEmailQuotePrefix(lines[previous])),
			) {
				validatedSessionFooter = reference != ""
				for candidate := previous; candidate < index; candidate++ {
					remove[candidate] = true
				}
				separator := previous - 1
				for separator >= 0 && strings.TrimSpace(stripEmailQuotePrefix(lines[separator])) == "" {
					separator--
				}
				if separator >= 0 && conversationFooterRuleLine.MatchString(
					strings.TrimSpace(stripEmailQuotePrefix(lines[separator])),
				) {
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
		if next < len(lines) {
			nextContent := strings.TrimSpace(stripEmailQuotePrefix(lines[next]))
			legacyMotto := strings.EqualFold(nextContent, conversationFooterMotto)
			labeledQuote := validatedSessionFooter && conversationFooterQuoteLine.MatchString(nextContent)
			if legacyMotto || labeledQuote {
				for candidate := index + 1; candidate <= next; candidate++ {
					remove[candidate] = true
				}
			}
			if labeledQuote && conversationFooterQuoteLabelLine.MatchString(nextContent) {
				quotedText := next + 1
				for quotedText < len(lines) && strings.TrimSpace(stripEmailQuotePrefix(lines[quotedText])) == "" {
					quotedText++
				}
				if quotedText < len(lines) && conversationFooterQuotedTextLine.MatchString(
					strings.TrimSpace(stripEmailQuotePrefix(lines[quotedText])),
				) {
					for candidate := next + 1; candidate <= quotedText; candidate++ {
						remove[candidate] = true
					}
				}
			}
		}
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
