package client

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultAttachmentLimits(t *testing.T) {
	got := DefaultAttachmentLimits()
	if got.MaxFileBytes != 7<<20 || got.MaxTotalBytes != 7<<20 || got.MaxCount != 10 {
		t.Fatalf("DefaultAttachmentLimits() = %+v, want MaxFileBytes=7<<20 MaxTotalBytes=7<<20 MaxCount=10", got)
	}
}

func TestTurnKeyIsStableAndPathSafe(t *testing.T) {
	first := TurnKey(3, "message-42")
	second := TurnKey(3, "message-42")
	if first != second {
		t.Fatalf("TurnKey not stable: %q vs %q", first, second)
	}
	if !strings.HasPrefix(first, "s3-") {
		t.Fatalf("TurnKey = %q, want prefix s3-", first)
	}
	hexPart := strings.TrimPrefix(first, "s3-")
	if len(hexPart) != 12 {
		t.Fatalf("TurnKey = %q, want 12 hex chars after prefix, got %d", first, len(hexPart))
	}
	for _, r := range hexPart {
		if r < '0' || r > '9' && r < 'a' || r > 'f' {
			t.Fatalf("TurnKey = %q, hex part %q is not lowercase hex", first, hexPart)
		}
	}
	if strings.ContainsAny(first, `/\`) {
		t.Fatalf("TurnKey = %q contains path separator", first)
	}
	empty := TurnKey(0, "")
	if !strings.HasPrefix(empty, "s0-") || strings.ContainsAny(empty, `/\`) {
		t.Fatalf("TurnKey empty messageID = %q", empty)
	}
}

func TestNormalizeFilenameRejectsUnsafeInputs(t *testing.T) {
	unsafe := []string{
		"",
		".",
		"..",
		"a/b",
		`a\b`,
		`..\evil`,
		"../evil",
		"con",
		"CON",
		"con.txt",
		"lpt9.doc",
		"a\x00b",
		"a\x1fb",
		"report.",
	}
	for _, name := range unsafe {
		t.Run(fmt.Sprintf("reject_%q", name), func(t *testing.T) {
			got, err := normalizeFilename(name)
			if err == nil || !strings.Contains(err.Error(), "unsafe attachment filename") {
				t.Fatalf("normalizeFilename(%q) = %q, %v; want error containing %q", name, got, err, "unsafe attachment filename")
			}
		})
	}

	safe := []struct {
		in   string
		want string
	}{
		{in: "  report.txt  ", want: "report.txt"},
		{in: "plain.txt", want: "plain.txt"},
		{in: "report ", want: "report"},
	}
	for _, test := range safe {
		t.Run(fmt.Sprintf("accept_%q", test.in), func(t *testing.T) {
			got, err := normalizeFilename(test.in)
			if err != nil || got != test.want {
				t.Fatalf("normalizeFilename(%q) = %q, %v; want %q", test.in, got, err, test.want)
			}
		})
	}
}

func TestStageInboxWritesFilesAndManifest(t *testing.T) {
	projectDir := t.TempDir()
	turnKey := TurnKey(1, "inbound-1")
	transport := newFakeTransport()
	transport.attachments["attachment-1"] = []byte("hello")
	transport.attachments["attachment-2"] = []byte("hi mark")
	refs := []AttachmentRef{
		{
			AttachmentID: "attachment-1",
			Filename:     "hello.txt",
			ContentType:  "text/plain",
			SizeBytes:    5,
		},
		{
			AttachmentID: "attachment-2",
			Filename:     "notes.md",
			ContentType:  "text/markdown",
			SizeBytes:    7,
		},
	}

	dirs, manifest, err := StageInbox(context.Background(), transport, projectDir, turnKey, refs, DefaultAttachmentLimits())
	if err != nil {
		t.Fatalf("StageInbox: %v", err)
	}
	if manifest == nil {
		t.Fatal("StageInbox returned nil manifest")
	}
	if manifest.TurnKey != turnKey {
		t.Fatalf("manifest.TurnKey = %q, want %q", manifest.TurnKey, turnKey)
	}
	if len(manifest.Files) != 2 {
		t.Fatalf("manifest.Files length = %d, want 2", len(manifest.Files))
	}

	assertDirMode(t, dirs.Inbox, 0o700)
	assertDirMode(t, dirs.Outbox, 0o700)

	helloPath := filepath.Join(dirs.Inbox, "hello.txt")
	notesPath := filepath.Join(dirs.Inbox, "notes.md")
	assertRegularFile(t, helloPath, 0o600, []byte("hello"))
	assertRegularFile(t, notesPath, 0o600, []byte("hi mark"))

	raw, err := os.ReadFile(filepath.Join(dirs.Inbox, InboundManifestName()))
	if err != nil {
		t.Fatalf("read inbound manifest: %v", err)
	}
	var parsed InboundManifest
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("parse inbound manifest: %v", err)
	}
	if parsed.TurnKey != turnKey || len(parsed.Files) != 2 {
		t.Fatalf("parsed manifest = %+v", parsed)
	}

	wantSHA := []string{sha256Hex([]byte("hello")), sha256Hex([]byte("hi mark"))}
	wantNames := []string{"hello.txt", "notes.md"}
	wantTypes := []string{"text/plain", "text/markdown"}
	wantSizes := []int64{5, 7}
	wantIDs := []string{"attachment-1", "attachment-2"}
	for i, artifact := range manifest.Files {
		if artifact.AttachmentID != wantIDs[i] ||
			artifact.Filename != wantNames[i] ||
			artifact.ContentType != wantTypes[i] ||
			artifact.SizeBytes != wantSizes[i] ||
			artifact.SHA256 != wantSHA[i] {
			t.Fatalf("artifact[%d] = %+v", i, artifact)
		}
		if parsed.Files[i] != artifact {
			t.Fatalf("parsed artifact[%d] = %+v, want %+v", i, parsed.Files[i], artifact)
		}
	}
}

func TestStageInboxRejectsOversizeMetadata(t *testing.T) {
	projectDir := t.TempDir()
	turnKey := TurnKey(1, "oversize-meta")
	transport := newFakeTransport()
	refs := []AttachmentRef{{
		AttachmentID: "huge",
		Filename:     "huge.bin",
		ContentType:  "application/octet-stream",
		SizeBytes:    8 << 20,
	}}

	_, _, err := StageInbox(context.Background(), transport, projectDir, turnKey, refs, DefaultAttachmentLimits())
	if err == nil || !strings.Contains(err.Error(), "attachment size") {
		t.Fatalf("StageInbox error = %v, want attachment size", err)
	}
	assertStagingRemoved(t, projectDir, turnKey)
}

func TestStageInboxRejectsOversizeFetchedBytes(t *testing.T) {
	projectDir := t.TempDir()
	turnKey := TurnKey(1, "oversize-fetch")
	transport := newFakeTransport()
	transport.attachments["big"] = bytes.Repeat([]byte{'x'}, 100)
	refs := []AttachmentRef{{
		AttachmentID: "big",
		Filename:     "big.bin",
		ContentType:  "application/octet-stream",
	}}
	limits := AttachmentLimits{MaxFileBytes: 10, MaxTotalBytes: 1 << 20, MaxCount: 10}

	_, _, err := StageInbox(context.Background(), transport, projectDir, turnKey, refs, limits)
	if err == nil || (!strings.Contains(err.Error(), "big") && !strings.Contains(err.Error(), "too large")) {
		t.Fatalf("StageInbox error = %v, want big or too large", err)
	}
	assertStagingRemoved(t, projectDir, turnKey)
}

func TestStageInboxRejectsDuplicateNormalizedName(t *testing.T) {
	projectDir := t.TempDir()
	turnKey := TurnKey(1, "duplicate-name")
	transport := newFakeTransport()
	transport.attachments["a"] = []byte("one")
	transport.attachments["b"] = []byte("two")
	refs := []AttachmentRef{
		{AttachmentID: "a", Filename: "b.txt", SizeBytes: 3},
		{AttachmentID: "b", Filename: " b.txt ", SizeBytes: 3},
	}

	_, _, err := StageInbox(context.Background(), transport, projectDir, turnKey, refs, DefaultAttachmentLimits())
	if err == nil || !strings.Contains(err.Error(), "duplicate attachment filename") {
		t.Fatalf("StageInbox error = %v, want duplicate attachment filename", err)
	}
	assertStagingRemoved(t, projectDir, turnKey)
}

func TestStageInboxRejectsTooManyAttachments(t *testing.T) {
	projectDir := t.TempDir()
	turnKey := TurnKey(1, "too-many")
	transport := newFakeTransport()
	refs := make([]AttachmentRef, 11)
	for i := range refs {
		id := fmt.Sprintf("att-%d", i)
		transport.attachments[id] = []byte("x")
		refs[i] = AttachmentRef{
			AttachmentID: id,
			Filename:     fmt.Sprintf("f%d.txt", i),
			SizeBytes:    1,
		}
	}

	_, _, err := StageInbox(context.Background(), transport, projectDir, turnKey, refs, DefaultAttachmentLimits())
	if err == nil || !strings.Contains(err.Error(), "attachment count") {
		t.Fatalf("StageInbox error = %v, want attachment count", err)
	}
	assertStagingRemoved(t, projectDir, turnKey)
}

func TestStageInboxRejectsAggregateOverflow(t *testing.T) {
	projectDir := t.TempDir()
	turnKey := TurnKey(1, "aggregate")
	transport := newFakeTransport()
	transport.attachments["a"] = []byte("four")
	transport.attachments["b"] = []byte("four")
	refs := []AttachmentRef{
		{AttachmentID: "a", Filename: "a.txt", SizeBytes: 4},
		{AttachmentID: "b", Filename: "b.txt", SizeBytes: 4},
	}
	limits := AttachmentLimits{MaxFileBytes: 10, MaxTotalBytes: 6, MaxCount: 10}

	_, _, err := StageInbox(context.Background(), transport, projectDir, turnKey, refs, limits)
	if err == nil || !strings.Contains(err.Error(), "attachment size") {
		t.Fatalf("StageInbox error = %v, want attachment size", err)
	}
	assertStagingRemoved(t, projectDir, turnKey)
}

func TestCollectOutboxReturnsSortedDeterministicManifest(t *testing.T) {
	projectDir := t.TempDir()
	turnKey := TurnKey(4, "collect-sort")
	staging, err := stagePaths(projectDir, turnKey)
	if err != nil {
		t.Fatalf("stagePaths: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staging.Outbox, "b.txt"), []byte("bb"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging.Outbox, "a.md"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}

	files, manifest, err := CollectOutbox(context.Background(), staging, DefaultAttachmentLimits())
	if err != nil {
		t.Fatalf("CollectOutbox: %v", err)
	}
	if manifest == nil {
		t.Fatal("CollectOutbox returned nil manifest")
	}
	if manifest.TurnKey != filepath.Base(staging.Outbox) {
		t.Fatalf("manifest.TurnKey = %q, want %q", manifest.TurnKey, filepath.Base(staging.Outbox))
	}
	if len(files) != 2 || len(manifest.Files) != 2 {
		t.Fatalf("files=%d manifest.Files=%d, want 2", len(files), len(manifest.Files))
	}
	if files[0].Filename != "a.md" || files[1].Filename != "b.txt" {
		t.Fatalf("files order = %q, %q; want a.md, b.txt", files[0].Filename, files[1].Filename)
	}
	if manifest.Files[0].Filename != "a.md" || manifest.Files[1].Filename != "b.txt" {
		t.Fatalf("manifest order = %q, %q; want a.md, b.txt", manifest.Files[0].Filename, manifest.Files[1].Filename)
	}
	if string(files[0].Contents) != "a" || string(files[1].Contents) != "bb" {
		t.Fatalf("file contents = %q, %q", files[0].Contents, files[1].Contents)
	}
	if !strings.HasPrefix(files[0].ContentType, "text/plain") || !strings.HasPrefix(files[1].ContentType, "text/plain") {
		t.Fatalf("content types = %q, %q", files[0].ContentType, files[1].ContentType)
	}
	if files[0].ContentType != "text/plain" || files[1].ContentType != "text/plain" {
		t.Fatalf("stored content types = %q, %q; want text/plain", files[0].ContentType, files[1].ContentType)
	}
	if manifest.Files[0].SHA256 != sha256Hex([]byte("a")) || manifest.Files[1].SHA256 != sha256Hex([]byte("bb")) {
		t.Fatalf("sha256 = %q, %q", manifest.Files[0].SHA256, manifest.Files[1].SHA256)
	}
	if manifest.Files[0].SizeBytes != 1 || manifest.Files[1].SizeBytes != 2 {
		t.Fatalf("sizes = %d, %d", manifest.Files[0].SizeBytes, manifest.Files[1].SizeBytes)
	}

	first, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("manifest marshal not deterministic:\n%s\n%s", first, second)
	}
}

func TestCollectOutboxRejectsSymlink(t *testing.T) {
	projectDir := t.TempDir()
	turnKey := TurnKey(1, "symlink")
	staging, err := stagePaths(projectDir, turnKey)
	if err != nil {
		t.Fatalf("stagePaths: %v", err)
	}
	real := filepath.Join(staging.Outbox, "real.txt")
	if err := os.WriteFile(real, []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(staging.Outbox, "link.txt")); err != nil {
		t.Fatal(err)
	}

	_, _, err = CollectOutbox(context.Background(), staging, DefaultAttachmentLimits())
	if err == nil || !strings.Contains(err.Error(), "unsafe outbox entry") {
		t.Fatalf("CollectOutbox error = %v, want unsafe outbox entry", err)
	}
}

func TestCollectOutboxRejectsNestedDirectory(t *testing.T) {
	projectDir := t.TempDir()
	turnKey := TurnKey(1, "nested-dir")
	staging, err := stagePaths(projectDir, turnKey)
	if err != nil {
		t.Fatalf("stagePaths: %v", err)
	}
	if err := os.Mkdir(filepath.Join(staging.Outbox, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}

	_, _, err = CollectOutbox(context.Background(), staging, DefaultAttachmentLimits())
	if err == nil || !strings.Contains(err.Error(), "unsafe outbox entry") {
		t.Fatalf("CollectOutbox error = %v, want unsafe outbox entry", err)
	}
}

func TestCollectOutboxRejectsOversizeFile(t *testing.T) {
	projectDir := t.TempDir()
	turnKey := TurnKey(1, "outbox-oversize")
	staging, err := stagePaths(projectDir, turnKey)
	if err != nil {
		t.Fatalf("stagePaths: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staging.Outbox, "big.txt"), []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	limits := AttachmentLimits{MaxFileBytes: 2, MaxTotalBytes: 1 << 20, MaxCount: 10}

	_, _, err = CollectOutbox(context.Background(), staging, limits)
	if err == nil || !strings.Contains(err.Error(), "attachment size") {
		t.Fatalf("CollectOutbox error = %v, want attachment size", err)
	}
}

func TestCollectOutboxRejectsTooManyFiles(t *testing.T) {
	projectDir := t.TempDir()
	turnKey := TurnKey(1, "outbox-too-many")
	staging, err := stagePaths(projectDir, turnKey)
	if err != nil {
		t.Fatalf("stagePaths: %v", err)
	}
	for i := 0; i < 11; i++ {
		path := filepath.Join(staging.Outbox, fmt.Sprintf("f%d.txt", i))
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	_, _, err = CollectOutbox(context.Background(), staging, DefaultAttachmentLimits())
	if err == nil || !strings.Contains(err.Error(), "attachment count") {
		t.Fatalf("CollectOutbox error = %v, want attachment count", err)
	}
}

func TestCollectOutboxRejectsUnsafeFilename(t *testing.T) {
	tests := []string{"COM1.txt", "x\x7f.txt"}
	for _, name := range tests {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			projectDir := t.TempDir()
			turnKey := TurnKey(1, "unsafe-"+hex.EncodeToString([]byte(name)))
			staging, err := stagePaths(projectDir, turnKey)
			if err != nil {
				t.Fatalf("stagePaths: %v", err)
			}
			if err := os.WriteFile(filepath.Join(staging.Outbox, name), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, _, err = CollectOutbox(context.Background(), staging, DefaultAttachmentLimits())
			if err == nil || !strings.Contains(err.Error(), "unsafe attachment filename") {
				t.Fatalf("CollectOutbox error = %v, want unsafe attachment filename", err)
			}
		})
	}
}

func TestStageInboxPreparesStagingTwiceIdempotently(t *testing.T) {
	projectDir := t.TempDir()
	turnKey := TurnKey(2, "idempotent")
	first, err := stagePaths(projectDir, turnKey)
	if err != nil {
		t.Fatalf("stagePaths first: %v", err)
	}
	marker := filepath.Join(first.Inbox, "keep.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	second, err := stagePaths(projectDir, turnKey)
	if err != nil {
		t.Fatalf("stagePaths second: %v", err)
	}
	if first.Inbox != second.Inbox || first.Outbox != second.Outbox {
		t.Fatalf("stagePaths paths changed: %+v vs %+v", first, second)
	}
	got, err := os.ReadFile(marker)
	if err != nil || string(got) != "keep" {
		t.Fatalf("existing file = %q, %v; want keep", got, err)
	}
}

func TestBuildArchiveIsDeterministic(t *testing.T) {
	a := []byte("# a\n")
	b := []byte("hello")
	manifest := OutboundManifest{
		TurnKey: "s1-abc",
		Files: []StagedArtifact{
			{Filename: "a.md", ContentType: "text/markdown", SizeBytes: int64(len(a)), SHA256: sha256Hex(a)},
			{Filename: "b.txt", ContentType: "text/plain", SizeBytes: int64(len(b)), SHA256: sha256Hex(b)},
		},
	}
	files := []OutboundFile{
		{Filename: "a.md", ContentType: "text/markdown", Contents: a},
		{Filename: "b.txt", ContentType: "text/plain", Contents: b},
	}

	first, err := BuildArchive(manifest, files)
	if err != nil {
		t.Fatalf("BuildArchive first: %v", err)
	}
	second, err := BuildArchive(manifest, files)
	if err != nil {
		t.Fatalf("BuildArchive second: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("BuildArchive outputs are not byte-identical")
	}
	if sha256Hex(first) != sha256Hex(second) {
		t.Fatalf("BuildArchive sha256 mismatch: %s vs %s", sha256Hex(first), sha256Hex(second))
	}

	reader, err := zip.NewReader(bytes.NewReader(first), int64(len(first)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	wantNames := []string{"manifest.json", "a.md", "b.txt"}
	if len(reader.File) != len(wantNames) {
		t.Fatalf("archive entries = %d, want %d", len(reader.File), len(wantNames))
	}
	wantTime := time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
	wantContents := map[string][]byte{
		"a.md":  a,
		"b.txt": b,
	}
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("marshal expected manifest: %v", err)
	}
	manifestJSON = append(manifestJSON, '\n')
	wantContents["manifest.json"] = manifestJSON

	for i, file := range reader.File {
		if file.Name != wantNames[i] {
			t.Fatalf("entry[%d] = %q, want %q", i, file.Name, wantNames[i])
		}
		if !file.Modified.Equal(wantTime) {
			t.Fatalf("entry %s Modified = %v, want %v", file.Name, file.Modified, wantTime)
		}
		rc, err := file.Open()
		if err != nil {
			t.Fatalf("open %s: %v", file.Name, err)
		}
		got, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatalf("read %s: %v", file.Name, err)
		}
		want := wantContents[file.Name]
		if !bytes.Equal(got, want) {
			t.Fatalf("%s contents = %q, want %q", file.Name, got, want)
		}
		if file.CRC32 != crc32.ChecksumIEEE(want) {
			t.Fatalf("%s CRC32 = %08x, want %08x", file.Name, file.CRC32, crc32.ChecksumIEEE(want))
		}
		if file.Name == "manifest.json" {
			var parsed OutboundManifest
			if err := json.Unmarshal(got, &parsed); err != nil {
				t.Fatalf("parse manifest.json: %v", err)
			}
			if parsed.TurnKey != manifest.TurnKey || len(parsed.Files) != len(manifest.Files) {
				t.Fatalf("parsed manifest = %+v, want %+v", parsed, manifest)
			}
			for index, artifact := range manifest.Files {
				if parsed.Files[index] != artifact {
					t.Fatalf("parsed artifact[%d] = %+v, want %+v", index, parsed.Files[index], artifact)
				}
			}
		}
	}
}

func TestBuildArchiveRejectsMismatchedManifest(t *testing.T) {
	files := []OutboundFile{{Filename: "a.txt", Contents: []byte("x")}}
	manifest := OutboundManifest{
		TurnKey: "s1-abc",
		Files:   []StagedArtifact{{Filename: "x.txt", SizeBytes: 1, SHA256: sha256Hex([]byte("x"))}},
	}
	if _, err := BuildArchive(manifest, files); err == nil || !strings.Contains(err.Error(), "archive manifest mismatch") {
		t.Fatalf("missing file error = %v, want archive manifest mismatch", err)
	}

	emptyManifest := OutboundManifest{TurnKey: "s1-abc"}
	if _, err := BuildArchive(emptyManifest, files); err == nil || !strings.Contains(err.Error(), "archive manifest mismatch") {
		t.Fatalf("empty manifest error = %v, want archive manifest mismatch", err)
	}

	if _, err := BuildArchive(manifest, nil); err == nil || !strings.Contains(err.Error(), "archive needs files") {
		t.Fatalf("empty files error = %v, want archive needs files", err)
	}
}

func TestVerifyAndLoadOutboxRoundTripsAndDetectsTamper(t *testing.T) {
	projectDir := t.TempDir()
	turnKey := TurnKey(1, "verify-outbox")
	staging, err := stagePaths(projectDir, turnKey)
	if err != nil {
		t.Fatalf("stagePaths: %v", err)
	}
	a := []byte("alpha")
	b := []byte("bravo")
	if err := os.WriteFile(filepath.Join(staging.Outbox, "a.md"), a, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging.Outbox, "b.txt"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := OutboundManifest{
		TurnKey: turnKey,
		Files: []StagedArtifact{
			{Filename: "a.md", ContentType: "text/markdown", SizeBytes: int64(len(a)), SHA256: sha256Hex(a)},
			{Filename: "b.txt", ContentType: "text/plain", SizeBytes: int64(len(b)), SHA256: sha256Hex(b)},
		},
	}

	files, err := VerifyAndLoadOutbox(context.Background(), staging, manifest)
	if err != nil {
		t.Fatalf("VerifyAndLoadOutbox: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("files = %d, want 2", len(files))
	}
	if files[0].Filename != "a.md" || files[1].Filename != "b.txt" {
		t.Fatalf("order = %q, %q; want a.md, b.txt", files[0].Filename, files[1].Filename)
	}
	if !bytes.Equal(files[0].Contents, a) || !bytes.Equal(files[1].Contents, b) {
		t.Fatalf("contents = %q, %q", files[0].Contents, files[1].Contents)
	}
	if files[0].ContentType != "text/markdown" || files[1].ContentType != "text/plain" {
		t.Fatalf("content types = %q, %q", files[0].ContentType, files[1].ContentType)
	}

	if err := os.WriteFile(filepath.Join(staging.Outbox, "extra.bin"), []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	extra, err := VerifyAndLoadOutbox(context.Background(), staging, manifest)
	if err != nil {
		t.Fatalf("VerifyAndLoadOutbox with extra file: %v", err)
	}
	if len(extra) != 2 || extra[0].Filename != "a.md" || extra[1].Filename != "b.txt" {
		t.Fatalf("extra outbox file was not ignored: %+v", extra)
	}

	if err := os.WriteFile(filepath.Join(staging.Outbox, "a.md"), []byte("ALPHX"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyAndLoadOutbox(context.Background(), staging, manifest); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("tampered bytes error = %v, want changed", err)
	}

	if err := os.WriteFile(filepath.Join(staging.Outbox, "a.md"), a, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging.Outbox, "b.txt"), []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyAndLoadOutbox(context.Background(), staging, manifest); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("truncated file error = %v, want changed", err)
	}
}

func sha256Hex(contents []byte) string {
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:])
}

func assertDirMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", path)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}

func assertRegularFile(t *testing.T, path string, wantMode os.FileMode, wantBytes []byte) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("%s is not a regular file", path)
	}
	if got := info.Mode().Perm(); got != wantMode {
		t.Fatalf("%s mode = %o, want %o", path, got, wantMode)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !bytes.Equal(got, wantBytes) {
		t.Fatalf("%s contents = %q, want %q", path, got, wantBytes)
	}
}

func assertStagingRemoved(t *testing.T, projectDir, turnKey string) {
	t.Helper()
	inbox := filepath.Join(projectDir, ".attachments-inbox", turnKey)
	if _, err := os.Stat(inbox); !os.IsNotExist(err) {
		t.Fatalf("inbox %s still present: %v", inbox, err)
	}
	outbox := filepath.Join(projectDir, ".attachments-outbox", turnKey)
	if _, err := os.Stat(outbox); !os.IsNotExist(err) {
		t.Fatalf("outbox %s still present: %v", outbox, err)
	}
}
