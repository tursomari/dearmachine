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
	want := "----\nDear Machine:\nsession dm1-kyf1e-4cze7x"

	if got := conversationFooter(testCanonicalConversationReference); got != want {
		t.Fatalf("conversationFooter() = %q, want %q", got, want)
	}
	if strings.Contains(conversationFooter(testCanonicalConversationReference), "Ref:") {
		t.Fatalf("conversation footer retained legacy Ref line: %q", conversationFooter(testCanonicalConversationReference))
	}
}

func TestAppendConversationFooterRendersOptionalMagnificaQuote(t *testing.T) {
	tests := []struct {
		name  string
		quote *MagnificaHumanitas
		want  string
	}{
		{
			name: "stored quote",
			quote: &MagnificaHumanitas{
				Paragraph: 7,
				Line:      3,
				Quote:     "  Humanity is our finest work.  ",
			},
			want: "Answer.\n\n----\nDear Machine:\nsession dm1-kyf1e-4cze7x\n\nMagnifica Humanitas quote:\n\"Humanity is our finest work.\"",
		},
		{
			name: "no quote",
			want: "Answer.\n\n----\nDear Machine:\nsession dm1-kyf1e-4cze7x",
		},
		{
			name:  "empty durable quote",
			quote: &MagnificaHumanitas{Paragraph: 7, Line: 3, Quote: " \t\n "},
			want:  "Answer.\n\n----\nDear Machine:\nsession dm1-kyf1e-4cze7x",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := appendConversationFooter("Answer.\n", testCanonicalConversationReference, test.quote)
			if got != test.want {
				t.Fatalf("appendConversationFooter() = %q, want %q", got, test.want)
			}
			if gotMotto, wantMotto := strings.Contains(got, conversationFooterMotto), strings.Contains(test.want, conversationFooterMotto); gotMotto != wantMotto {
				t.Fatalf("footer motto presence = %t, want %t: %q", gotMotto, wantMotto, got)
			}
		})
	}
}

func TestStripConversationFootersOnlyRemovesQuoteFromValidatedFooter(t *testing.T) {
	tests := []struct {
		name           string
		body           string
		wantBody       string
		wantReferences []string
	}{
		{
			name: "validated footer with quote",
			body: "Continue.\n\n----\nDear Machine:\nsession dm1-kyf1e-4cze7x\n\n" +
				"Magnifica Humanitas quote:\n\"Humanity is our finest work.\"",
			wantBody:       "Continue.",
			wantReferences: []string{testCanonicalConversationReference},
		},
		{
			name:           "validated footer without quote",
			body:           "Continue.\n\n----\nDear Machine:\nsession dm1-kyf1e-4cze7x",
			wantBody:       "Continue.",
			wantReferences: []string{testCanonicalConversationReference},
		},
		{
			name:     "bare quote line",
			body:     "Keep this line.\nquote: this belongs to the message",
			wantBody: "Keep this line.\nquote: this belongs to the message",
		},
		{
			name: "wrong session id",
			body: "Keep this line.\n\n----\nDear Machine:\nsession dm1-invalid\n\n" +
				"Magnifica Humanitas quote:\n\"preserve after invalid session\"",
			wantBody: "Keep this line.\n\n----\nDear Machine:\nsession dm1-invalid\n\n" +
				"Magnifica Humanitas quote:\n\"preserve after invalid session\"",
		},
		{
			name: "altered header",
			body: "Keep this line.\n\n----\nDear Machines:\nsession dm1-kyf1e-4cze7x\n\n" +
				"Magnifica Humanitas quote:\n\"preserve after altered header\"",
			wantBody: "Keep this line.\n\n----\nDear Machines:\nsession dm1-kyf1e-4cze7x\n\n" +
				"Magnifica Humanitas quote:\n\"preserve after altered header\"",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotBody, gotReferences := stripConversationFooters(test.body)
			if gotBody != test.wantBody {
				t.Fatalf("clean body = %q, want %q", gotBody, test.wantBody)
			}
			if strings.Join(gotReferences, ",") != strings.Join(test.wantReferences, ",") {
				t.Fatalf("references = %v, want %v", gotReferences, test.wantReferences)
			}
		})
	}
}

func TestStripConversationFootersSupportsForwardedMultilineFooter(t *testing.T) {
	body := "Continue.\n\n> Earlier answer.\n> ----\n" +
		"> Dear Machine:\n> session dm1-kyf1e-4cze7x\n" +
		">\n" +
		"> Magnifica Humanitas quote:\n" +
		`> "Humanity is our finest work."`

	clean, references := stripConversationFooters(body)
	if clean != "Continue." {
		t.Fatalf("clean body = %q, want contribution only", clean)
	}
	if len(references) != 1 || references[0] != testCanonicalConversationReference {
		t.Fatalf("references = %v, want [%s]", references, testCanonicalConversationReference)
	}
	if strings.Contains(strings.ToLower(clean), "quote:") {
		t.Fatalf("clean body retained quote metadata:\n%s", clean)
	}
}

func TestStripConversationFootersPreservesAmbiguousForwardedContent(t *testing.T) {
	body := "Compare these forwarded answers.\n\n" +
		"> First answer.\n> ----\n> Dear Machine:\n> session dm1-kyf1e-4cze7x\n>\n" +
		"> Magnifica Humanitas quote:\n> \"Humanity is our finest work.\"\n\n" +
		">> Second answer.\n>> ----\n>> Dear Machine:\n>> session dm1-stvwx-yz0123"

	clean, references := stripConversationFooters(body)
	if got, want := references, []string{testCanonicalConversationReference, "dm1-stvwx-yz0123"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("references = %v, want %v", got, want)
	}
	for _, answer := range []string{"First answer.", "Second answer."} {
		if !strings.Contains(clean, answer) {
			t.Fatalf("ambiguous forward lost %q:\n%s", answer, clean)
		}
	}
	for _, metadata := range []string{"Dear Machine", "session dm1-", conversationFooterMotto + " quote:"} {
		if strings.Contains(clean, metadata) {
			t.Fatalf("clean body retained %q:\n%s", metadata, clean)
		}
	}
}

func TestConversationFooterExtractsStableReferenceAndStripsMetadata(t *testing.T) {
	reference := testCanonicalConversationReference
	body := "Please continue.\n\n> Earlier answer.\n> ----\n> Dear Machine:\n> session " + reference +
		"\n>\n> Magnifica Humanitas quote:\n> \"Humanity is our finest work.\"\n"

	clean, references := stripConversationFooters(body)
	if len(references) != 1 || references[0] != testCanonicalConversationReference {
		t.Fatalf("references = %v, want [%s]", references, testCanonicalConversationReference)
	}
	if clean != "Please continue." {
		t.Fatalf("clean body = %q, want only the new contribution", clean)
	}
	for _, metadata := range []string{"Dear Machine:", "Magnifica Humanitas", "\n> ----"} {
		if strings.Contains(clean, metadata) {
			t.Fatalf("clean body retained %q:\n%s", metadata, clean)
		}
	}
}

func TestConversationFooterPreservesQuotedContentForInvalidReference(t *testing.T) {
	body := "Please assess this excerpt.\n\n> Keep this quoted answer.\n> ----\n" +
		"> Dear Machine:\n> session dm1-invalid\n>\n" +
		"> Magnifica Humanitas quote:\n> \"preserve this\""

	clean, references := stripConversationFooters(body)
	if len(references) != 0 {
		t.Fatalf("references = %v, want none", references)
	}
	if !strings.Contains(clean, "Keep this quoted answer.") {
		t.Fatalf("invalid reference removed quoted content:\n%s", clean)
	}
}

func TestConversationFooterPreservesQuoteWithoutNewContribution(t *testing.T) {
	reference := newConversationReference()
	body := "> Earlier answer.\n> " + strings.ReplaceAll(conversationFooter(reference), "\n", "\n> ")

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

func TestStripConversationFootersRemovesQuotedSessionBlocksAndDeduplicatesDepths(t *testing.T) {
	footer := conversationFooter(testCanonicalConversationReference) +
		"\n\n" + conversationFooterMotto + " " + conversationFooterQuoteLabel + ":\n\"Humanity is our finest work.\""
	body := "New contribution.\n\n> Earlier answer.\n> " + strings.ReplaceAll(footer, "\n", "\n> ") +
		"\n\n>> Older answer.\n>> " + strings.ReplaceAll(footer, "\n", "\n>> ")

	clean, references := stripConversationFooters(body)
	if clean != "New contribution." {
		t.Fatalf("clean body = %q, want only new contribution", clean)
	}
	if len(references) != 1 || references[0] != testCanonicalConversationReference {
		t.Fatalf("references = %v, want one deduplicated short reference", references)
	}
	for _, metadata := range []string{"Dear Machine", "session dm1-", conversationFooterMotto, "> ----", ">> ----"} {
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
	if strings.Contains(message.Body, "Dear Machine:") || strings.Contains(message.Body, "session dm1-") {
		t.Fatalf("ambiguous footer metadata reached clean body:\n%s", message.Body)
	}
}

func TestPrepareInboundMessageDoesNotChooseBetweenDistinctShortReferences(t *testing.T) {
	body := "Compare these.\n\n" + conversationFooter(testCanonicalConversationReference) +
		"\n\n" + conversationFooter("dm1-stvwx-yz0123")

	message, reference := prepareInboundMessage(Message{Body: body})
	if reference != "" {
		t.Fatalf("ambiguous reference = %q, want empty", reference)
	}
	if got, want := message.ConversationReferences, []string{"dm1-kyf1e-4cze7x", "dm1-stvwx-yz0123"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
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

func TestPrepareInboundMessageKeepsOnlyNewlyAuthoredReplyAcrossTransportFormats(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "gmail and agentmail",
			body: "Are you running on the host or in a container?\n\n" +
				"On Wed, Sep 2, 2026 at 9:22 AM <machine@example.com> wrote:\n\n" +
				"> The working directory is /home/user/.dearmachine/entrypoint/main.\n" +
				"> Best, Dear Machine",
			want: "Are you running on the host or in a container?",
		},
		{
			name: "wrapped attribution",
			body: "Use the smaller model.\n\n" +
				"On Wed, Sep 2, 2026 at 9:22 AM Dear Machine\n" +
				"<machine@example.com> wrote:\n" +
				"> I can use either model.",
			want: "Use the smaller model.",
		},
		{
			name: "outlook and openmail",
			body: "Please proceed.\n\n________________________________\n" +
				"From: Dear Machine <machine@example.com>\n" +
				"Sent: Wednesday, September 2, 2026 9:22 AM\n" +
				"To: User <user@example.com>\n" +
				"Subject: Re: setup\n\nEarlier answer.",
			want: "Please proceed.",
		},
		{
			name: "original message and sendmux",
			body: "That works.\n\n-----Original Message-----\n" +
				"From: machine@example.com\nSubject: Re: setup\n\nEarlier answer.",
			want: "That works.",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message, _ := prepareInboundMessage(Message{Body: test.body})
			if message.Body != test.want {
				t.Fatalf("clean body = %q, want %q", message.Body, test.want)
			}
		})
	}
}

func TestPrepareInboundMessagePreservesReferenceFromDiscardedHistory(t *testing.T) {
	reference := newConversationReference()
	body := "Continue with the next section.\n\n" +
		"On Wed, Sep 2, 2026 at 9:22 AM <machine@example.com> wrote:\n\n" +
		"> Earlier answer.\n> " + strings.ReplaceAll(conversationFooter(reference), "\n", "\n> ")

	message, gotReference := prepareInboundMessage(Message{Body: body})
	if message.Body != "Continue with the next section." {
		t.Fatalf("clean body = %q", message.Body)
	}
	if gotReference != reference || len(message.ConversationReferences) != 1 {
		t.Fatalf("reference = %q, evidence = %v, want %q", gotReference, message.ConversationReferences, reference)
	}
}

func TestPrepareInboundMessageDoesNotStripOrdinaryQuotesOrForwardedMail(t *testing.T) {
	for _, body := range []string{
		"On reliability, Alice wrote:\nKeep this sentence because it is the user's whole message.",
		"Please compare these lines:\n\n> first option\n> second option",
		"Please review this.\n\nBegin forwarded message:\nFrom: alice@example.com\n\nOriginal material.",
		"On Wed, Sep 2, 2026 at 9:22 AM <machine@example.com> wrote:\n> This reply contains no newly authored text.",
	} {
		message, _ := prepareInboundMessage(Message{Body: body})
		if message.Body != body {
			t.Fatalf("body was unexpectedly stripped:\nwant: %q\n got: %q", body, message.Body)
		}
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
