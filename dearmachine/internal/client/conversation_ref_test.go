package client

import (
	"regexp"
	"strings"
	"testing"
)

const testConversationPayload = "KYF1E4CZE7XBCDEFGHJKMNPQRC"
const testCanonicalConversationReference = "dm1-kyf1e-4cze7x"

func TestNewConversationReferenceGeneratesUniqueCanonicalShortIDs(t *testing.T) {
	pattern := regexp.MustCompile(`^dm1-[0-9a-hjkmnp-tv-z]{5}-[0-9a-hjkmnp-tv-z]{6}$`)
	seen := make(map[string]struct{})
	for range 1_000 {
		reference := newConversationReference()
		if !pattern.MatchString(reference) {
			t.Fatalf("generated reference %q does not match canonical short format", reference)
		}
		if !validConversationReference(reference) || !isCanonicalConversationReference(reference) {
			t.Fatalf("generated reference %q is not canonical", reference)
		}
		if _, duplicate := seen[reference]; duplicate {
			t.Fatalf("generated duplicate reference %q", reference)
		}
		seen[reference] = struct{}{}
	}
}

func TestCanonicalConversationReferenceAcceptsCaseAndHyphenVariants(t *testing.T) {
	for _, input := range []string{
		"dm1-kyf1e-4cze7x",
		" DM1-KYF1E-4CZE7X ",
		"dm1kyf1e4cze7x",
		"d-m-1-k-y-f-1-e-4-c-z-e-7-x",
	} {
		if got := canonicalConversationReference(input); got != testCanonicalConversationReference {
			t.Errorf("canonicalConversationReference(%q) = %q, want %q", input, got, testCanonicalConversationReference)
		}
	}
	if got := shortConversationReference(testCanonicalConversationReference); got != testCanonicalConversationReference {
		t.Fatalf("shortConversationReference() = %q, want canonical identity", got)
	}
}

func TestCanonicalInboundReferenceMapsValidatedLegacyFullReference(t *testing.T) {
	legacy := legacyReferenceFromPayload(t, testConversationPayload)
	if got := canonicalInboundReference(strings.ToLower(legacy)); got != testCanonicalConversationReference {
		t.Fatalf("canonicalInboundReference(%q) = %q, want %q", legacy, got, testCanonicalConversationReference)
	}

	corrupted := legacy[:len(legacy)-1] + "0"
	if corrupted == legacy {
		corrupted = legacy[:len(legacy)-1] + "1"
	}
	for _, invalid := range []string{corrupted, legacy[:len(legacy)-1], legacy + "0"} {
		if got := canonicalInboundReference(invalid); got != "" {
			t.Errorf("canonicalInboundReference(%q) = %q, want empty", invalid, got)
		}
	}
}

func TestConversationFooterRendersShortSessionReference(t *testing.T) {
	want := "--\nDear Machine\nsession: dm1-kyf1e-4cze7x\nMagnifica Humanitas"

	if got := conversationFooter(testCanonicalConversationReference); got != want {
		t.Fatalf("conversationFooter() = %q, want %q", got, want)
	}
	if strings.Contains(conversationFooter(testCanonicalConversationReference), "Ref:") {
		t.Fatalf("conversation footer retained legacy Ref line: %q", conversationFooter(testCanonicalConversationReference))
	}
}

func TestConversationFooterExtractsStableReferenceAndStripsMetadata(t *testing.T) {
	reference := legacyReferenceFromPayload(t, testConversationPayload)
	body := "Please continue.\n\n> Earlier answer.\n> --\n> Dear Machine - Ref: " + reference + "\n> Magnifica Humanitas\n"

	clean, references := stripConversationFooters(body)
	if len(references) != 1 || references[0] != testCanonicalConversationReference {
		t.Fatalf("references = %v, want [%s]", references, testCanonicalConversationReference)
	}
	if clean != "Please continue." {
		t.Fatalf("clean body = %q, want only the new contribution", clean)
	}
	for _, metadata := range []string{"Dear Machine - Ref:", "Magnifica Humanitas", "\n> --"} {
		if strings.Contains(clean, metadata) {
			t.Fatalf("clean body retained %q:\n%s", metadata, clean)
		}
	}
}

func TestConversationFooterPreservesQuotedContentForInvalidReference(t *testing.T) {
	reference := legacyReferenceFromPayload(t, testConversationPayload)
	checksum := "0"
	if strings.HasSuffix(reference, checksum) {
		checksum = "1"
	}
	invalid := reference[:len(reference)-1] + checksum
	body := "Please assess this excerpt.\n\n> Keep this quoted answer.\n> --\n" +
		"> Dear Machine - Ref: " + invalid + "\n> Magnifica Humanitas"

	clean, references := stripConversationFooters(body)
	if len(references) != 0 {
		t.Fatalf("references = %v, want none", references)
	}
	if !strings.Contains(clean, "Keep this quoted answer.") {
		t.Fatalf("invalid reference removed quoted content:\n%s", clean)
	}
}

func TestConversationFooterPreservesAmbiguousForwardedContent(t *testing.T) {
	first := newConversationReference()
	second := newConversationReference()
	body := "Compare these forwarded answers.\n\n" +
		"> First answer.\n> --\n> Dear Machine - Ref: " + first + "\n> Magnifica Humanitas\n\n" +
		"> Second answer.\n> --\n> Dear Machine - Ref: " + second + "\n> Magnifica Humanitas"

	clean, references := stripConversationFooters(body)
	if len(references) != 2 {
		t.Fatalf("references = %v, want both forwarded references", references)
	}
	for _, answer := range []string{"First answer.", "Second answer."} {
		if !strings.Contains(clean, answer) {
			t.Fatalf("ambiguous forward lost %q:\n%s", answer, clean)
		}
	}
}

func TestConversationFooterPreservesQuoteWithoutNewContribution(t *testing.T) {
	reference := newConversationReference()
	body := "> Earlier answer.\n> --\n" +
		"> Dear Machine - Ref: " + reference + "\n> Magnifica Humanitas"

	clean, references := stripConversationFooters(body)
	if len(references) != 1 || references[0] != reference {
		t.Fatalf("references = %v, want [%s]", references, reference)
	}
	if !strings.Contains(clean, "Earlier answer.") {
		t.Fatalf("quote without a new contribution was removed:\n%s", clean)
	}
}

func TestConversationFooterDeduplicatesNestedStableReferences(t *testing.T) {
	reference := newConversationReference()
	footer := conversationFooter(reference)
	body := "Latest reply.\n\n> " + strings.ReplaceAll(footer, "\n", "\n> ") +
		"\n>> " + strings.ReplaceAll(footer, "\n", "\n>> ")

	_, references := stripConversationFooters(body)
	want := canonicalInboundReference(shortConversationReference(reference))
	if len(references) != 1 || references[0] != want {
		t.Fatalf("nested references = %v, want one stable reference", references)
	}
}

func TestCanonicalInboundReferenceAcceptsShortSessionTokens(t *testing.T) {
	validFull := legacyReferenceFromPayload(t, testConversationPayload)
	tests := []struct {
		name  string
		token string
		want  string
	}{
		{name: "lowercase displayed", token: "dm1-kyf1e-4cze7x", want: testCanonicalConversationReference},
		{name: "uppercase displayed", token: "DM1-KYF1E-4CZE7X", want: testCanonicalConversationReference},
		{name: "ungrouped short", token: " DM1-KYF1E4CZE7X ", want: testCanonicalConversationReference},
		{name: "valid full", token: strings.ToLower(validFull), want: testCanonicalConversationReference},
		{name: "missing prefix", token: "kyf1e-4cze7x"},
		{name: "short payload", token: "dm1-kyf1e-4cze7"},
		{name: "long payload", token: "dm1-kyf1e-4cze7xz"},
		{name: "invalid i", token: "dm1-kyf1i-4cze7x"},
		{name: "invalid l", token: "dm1-kyf1l-4cze7x"},
		{name: "invalid o", token: "dm1-kyf1o-4cze7x"},
		{name: "invalid u", token: "dm1-kyf1u-4cze7x"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := canonicalInboundReference(test.token); got != test.want {
				t.Fatalf("canonicalInboundReference(%q) = %q, want %q", test.token, got, test.want)
			}
		})
	}
}

func TestSessionFooterParsingIsCaseInsensitive(t *testing.T) {
	for _, label := range []string{"SESSION", "Session", "session"} {
		t.Run(label, func(t *testing.T) {
			body := "Continue.\n\n--\nDear Machine\n" + label + ": dm1-kyf1e-4cze7x\n" + conversationFooterMotto
			clean, references := stripConversationFooters(body)
			if clean != "Continue." {
				t.Fatalf("clean body = %q, want contribution only", clean)
			}
			if len(references) != 1 || references[0] != testCanonicalConversationReference {
				t.Fatalf("references = %v, want [%s]", references, testCanonicalConversationReference)
			}
		})
	}
}

func TestStripConversationFootersRemovesQuotedSessionBlocksAndDeduplicatesDepths(t *testing.T) {
	footer := "--\n\nDear Machine\n\nsession: dm1-kyf1e-4cze7x\n\n" + conversationFooterMotto
	body := "New contribution.\n\n> Earlier answer.\n> " + strings.ReplaceAll(footer, "\n", "\n> ") +
		"\n\n>> Older answer.\n>> " + strings.ReplaceAll(footer, "\n", "\n>> ")

	clean, references := stripConversationFooters(body)
	if clean != "New contribution." {
		t.Fatalf("clean body = %q, want only new contribution", clean)
	}
	if len(references) != 1 || references[0] != testCanonicalConversationReference {
		t.Fatalf("references = %v, want one deduplicated short reference", references)
	}
	for _, metadata := range []string{"Dear Machine", "session:", conversationFooterMotto, "> --", ">> --"} {
		if strings.Contains(clean, metadata) {
			t.Fatalf("clean body retained %q:\n%s", metadata, clean)
		}
	}
}

func TestPrepareInboundMessageDoesNotChooseBetweenDistinctReferences(t *testing.T) {
	first := newConversationReference()
	second := newConversationReference()
	message, reference := prepareInboundMessage(Message{Body: "Forwarded material.\n\n" + conversationFooter(first) + "\n\n" + conversationFooter(second)})
	if reference != "" {
		t.Fatalf("ambiguous reference = %q, want empty", reference)
	}
	if len(message.ConversationReferences) != 2 {
		t.Fatalf("references = %v, want both retained as evidence", message.ConversationReferences)
	}
	if strings.Contains(message.Body, "Dear Machine - Ref:") ||
		strings.Contains(message.Body, conversationFooterMotto) {
		t.Fatalf("ambiguous footer metadata reached clean body:\n%s", message.Body)
	}
}

func TestPrepareInboundMessageDoesNotChooseBetweenDistinctShortReferences(t *testing.T) {
	body := "Compare these.\n\n--\nDear Machine\nsession: dm1-kyf1e-4cze7x\n" + conversationFooterMotto +
		"\n\n--\nDear Machine\nsession: dm1-stvwx-yz0123\n" + conversationFooterMotto

	message, reference := prepareInboundMessage(Message{Body: body})
	if reference != "" {
		t.Fatalf("ambiguous reference = %q, want empty", reference)
	}
	if got, want := message.ConversationReferences, []string{"dm1-kyf1e-4cze7x", "dm1-stvwx-yz0123"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("references = %v, want %v", got, want)
	}
}

func TestPrepareInboundMessageRetainsLegacyFullAndDifferentShortReferences(t *testing.T) {
	full := legacyReferenceFromPayload(t, testConversationPayload)
	body := "Compare these.\n\n--\nDear Machine - Ref: " + full + "\n" + conversationFooterMotto +
		"\n\n--\nDear Machine\nsession: dm1-stvwx-yz0123\n" + conversationFooterMotto

	message, reference := prepareInboundMessage(Message{Body: body})
	if reference != "" {
		t.Fatalf("ambiguous reference = %q, want empty", reference)
	}
	if got, want := message.ConversationReferences, []string{testCanonicalConversationReference, "dm1-stvwx-yz0123"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("references = %v, want %v", got, want)
	}
}

func TestPrepareInboundMessageDeduplicatesLegacyFullAndItsShortToken(t *testing.T) {
	full := legacyReferenceFromPayload(t, testConversationPayload)
	message, reference := prepareInboundMessage(Message{
		ConversationReferences: []string{full, testCanonicalConversationReference},
	})
	if reference != testCanonicalConversationReference {
		t.Fatalf("full-plus-prefix reference = %q, want %q", reference, testCanonicalConversationReference)
	}
	if len(message.ConversationReferences) != 1 || message.ConversationReferences[0] != testCanonicalConversationReference {
		t.Fatalf("references = %v, want one canonical short token", message.ConversationReferences)
	}
}

func TestFormattedReplyFooterRoundTripsThroughHTMLNormalization(t *testing.T) {
	reference := newConversationReference()
	outbound := appendConversationFooter("Formatted answer.", reference)
	clean, references := stripConversationFooters(htmlToText(replyHTML(outbound)))
	if clean != "Formatted answer." {
		t.Fatalf("HTML-normalized answer = %q", clean)
	}
	want := canonicalInboundReference(shortConversationReference(reference))
	if len(references) != 1 || references[0] != want {
		t.Fatalf("HTML-normalized references = %v, want [%s]", references, want)
	}
}

func legacyReferenceFromPayload(t *testing.T, payload string) string {
	t.Helper()
	if len(payload) != 26 {
		t.Fatalf("test payload length = %d, want 26", len(payload))
	}
	reference := conversationReferencePrefix + payload + string(conversationReferenceChecksum(payload))
	if canonicalInboundReference(reference) == "" {
		t.Fatalf("constructed invalid legacy test reference %q", reference)
	}
	return reference
}
