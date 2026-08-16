package client

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	inboundManifestName   = "inbound-manifest.json"
	outboundManifestName  = "outbound-manifest.json"
	attachmentArchiveName = "attachments.zip"
	outboundManifestEntry = "manifest.json"
)

func InboundManifestName() string   { return inboundManifestName }
func OutboundManifestName() string  { return outboundManifestName }
func AttachmentArchiveName() string { return attachmentArchiveName }

type AttachmentLimits struct {
	MaxFileBytes  int64
	MaxTotalBytes int64
	MaxCount      int
}

func DefaultAttachmentLimits() AttachmentLimits {
	return AttachmentLimits{
		MaxFileBytes:  7 << 20,
		MaxTotalBytes: 7 << 20,
		MaxCount:      10,
	}
}

type StagedArtifact struct {
	AttachmentID string `json:"attachment_id,omitempty"`
	Filename     string `json:"filename"`
	ContentType  string `json:"content_type"`
	SizeBytes    int64  `json:"size_bytes"`
	SHA256       string `json:"sha256"`
}

type InboundManifest struct {
	TurnKey string           `json:"turn_key"`
	Files   []StagedArtifact `json:"files"`
}

type OutboundManifest struct {
	TurnKey string           `json:"turn_key"`
	Files   []StagedArtifact `json:"files"`
}

type StagingDirs struct {
	ProjectDir string `json:"project_dir"`
	Inbox      string `json:"-"`
	Outbox     string `json:"-"`
}

func TurnKey(sequence int, messageID string) string {
	sum := sha256.Sum256([]byte(messageID))
	return fmt.Sprintf("s%d-%x", sequence, sum[:6])
}

func normalizeFilename(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." ||
		strings.ContainsAny(name, `/\`) ||
		strings.HasSuffix(name, ".") ||
		containsControlRune(name) ||
		isWindowsReservedName(name) {
		return "", fmt.Errorf("unsafe attachment filename %q", name)
	}
	return name, nil
}

func containsControlRune(name string) bool {
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func isWindowsReservedName(name string) bool {
	stem := name
	if index := strings.IndexByte(name, '.'); index >= 0 {
		stem = name[:index]
	}
	switch strings.ToUpper(stem) {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return true
	default:
		return false
	}
}

func stagePaths(projectDir, turnKey string) (StagingDirs, error) {
	if strings.TrimSpace(projectDir) == "" {
		return StagingDirs{}, fmt.Errorf("project directory is empty")
	}
	projectAbs, err := filepath.Abs(filepath.Clean(projectDir))
	if err != nil {
		return StagingDirs{}, fmt.Errorf("resolve project directory: %w", err)
	}

	inboxRoot := filepath.Join(projectAbs, ".attachments-inbox")
	outboxRoot := filepath.Join(projectAbs, ".attachments-outbox")
	inbox := filepath.Join(inboxRoot, turnKey)
	outbox := filepath.Join(outboxRoot, turnKey)

	if !pathInside(projectAbs, inboxRoot) || !pathInside(projectAbs, outboxRoot) ||
		!pathInside(inboxRoot, inbox) || !pathInside(outboxRoot, outbox) {
		return StagingDirs{}, fmt.Errorf("staging path escapes project directory")
	}

	if err := os.MkdirAll(inbox, 0o700); err != nil {
		return StagingDirs{}, fmt.Errorf("create inbox %s: %w", inbox, err)
	}
	if err := os.MkdirAll(outbox, 0o700); err != nil {
		return StagingDirs{}, fmt.Errorf("create outbox %s: %w", outbox, err)
	}

	return StagingDirs{ProjectDir: projectAbs, Inbox: inbox, Outbox: outbox}, nil
}

// RemoveStagingDirs removes the turn-scoped inbound and outbox staging
// directories for turnKey below projectDir. Absent directories are not an
// error, so it is safe to call for turns that never staged attachments.
func RemoveStagingDirs(projectDir, turnKey string) error {
	if strings.TrimSpace(projectDir) == "" {
		return fmt.Errorf("project directory is empty")
	}
	if strings.TrimSpace(turnKey) == "" {
		return fmt.Errorf("turn key is empty")
	}
	inbox := filepath.Join(projectDir, ".attachments-inbox", turnKey)
	outbox := filepath.Join(projectDir, ".attachments-outbox", turnKey)
	var first error
	for _, path := range []string{inbox, outbox} {
		if err := os.RemoveAll(path); err != nil {
			first = errors.Join(first, fmt.Errorf("remove staging dir %s: %w", path, err))
		}
	}
	return first
}

func pathInside(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, "..")
}

func normalizeLimits(limits AttachmentLimits) AttachmentLimits {
	defaults := DefaultAttachmentLimits()
	if limits.MaxFileBytes <= 0 {
		limits.MaxFileBytes = defaults.MaxFileBytes
	}
	if limits.MaxTotalBytes <= 0 {
		limits.MaxTotalBytes = defaults.MaxTotalBytes
	}
	if limits.MaxCount <= 0 {
		limits.MaxCount = defaults.MaxCount
	}
	return limits
}

func StageInbox(
	ctx context.Context,
	transport Transport,
	projectDir, turnKey string,
	attachments []AttachmentRef,
	limits AttachmentLimits,
) (StagingDirs, *InboundManifest, error) {
	limits = normalizeLimits(limits)
	if turnKey == "" {
		return StagingDirs{}, nil, fmt.Errorf("turn key is empty")
	}
	if len(attachments) > limits.MaxCount {
		return StagingDirs{}, nil, fmt.Errorf("attachment count %d exceeds limit %d", len(attachments), limits.MaxCount)
	}

	normalized := make([]string, len(attachments))
	seen := make(map[string]struct{}, len(attachments))
	var recordedTotal int64
	allPositive := len(attachments) > 0
	for index, ref := range attachments {
		name, err := normalizeFilename(ref.Filename)
		if err != nil {
			return StagingDirs{}, nil, fmt.Errorf("%s: %w", ref.Filename, err)
		}
		if _, exists := seen[name]; exists {
			return StagingDirs{}, nil, fmt.Errorf("duplicate attachment filename %s", name)
		}
		seen[name] = struct{}{}
		normalized[index] = name
		if ref.SizeBytes > 0 {
			if ref.SizeBytes > limits.MaxFileBytes {
				return StagingDirs{}, nil, fmt.Errorf("attachment size %d exceeds limit", ref.SizeBytes)
			}
			recordedTotal += ref.SizeBytes
		} else {
			allPositive = false
		}
	}
	if allPositive && recordedTotal > limits.MaxTotalBytes {
		return StagingDirs{}, nil, fmt.Errorf("attachment size %d exceeds limit", recordedTotal)
	}

	dirs, err := stagePaths(projectDir, turnKey)
	if err != nil {
		return StagingDirs{}, nil, err
	}

	manifest, err := writeInbox(ctx, transport, dirs, turnKey, attachments, normalized, limits)
	if err != nil {
		_ = os.RemoveAll(dirs.Inbox)
		_ = os.RemoveAll(dirs.Outbox)
		return StagingDirs{}, nil, err
	}
	return dirs, manifest, nil
}

func writeInbox(
	ctx context.Context,
	transport Transport,
	dirs StagingDirs,
	turnKey string,
	attachments []AttachmentRef,
	normalized []string,
	limits AttachmentLimits,
) (*InboundManifest, error) {
	artifacts := make([]StagedArtifact, 0, len(attachments))
	var total int64
	for index, ref := range attachments {
		contents, err := transport.FetchAttachment(ctx, ref.AttachmentID, limits.MaxFileBytes)
		if err != nil {
			return nil, fmt.Errorf("fetch attachment %s: %w", ref.AttachmentID, err)
		}
		size := int64(len(contents))
		if size > limits.MaxFileBytes {
			return nil, fmt.Errorf("attachment %s: attachment size %d exceeds limit", ref.AttachmentID, size)
		}
		total += size
		if total > limits.MaxTotalBytes {
			return nil, fmt.Errorf("attachment size %d exceeds limit", total)
		}

		if err := writeFileAtomic(dirs.Inbox, normalized[index], contents); err != nil {
			return nil, fmt.Errorf("write attachment %s: %w", ref.AttachmentID, err)
		}

		sum := sha256.Sum256(contents)
		artifacts = append(artifacts, StagedArtifact{
			AttachmentID: ref.AttachmentID,
			Filename:     normalized[index],
			ContentType:  storedContentType(ref.ContentType, contents),
			SizeBytes:    size,
			SHA256:       hex.EncodeToString(sum[:]),
		})
	}

	manifest := InboundManifest{TurnKey: turnKey, Files: artifacts}
	if manifest.Files == nil {
		manifest.Files = []StagedArtifact{}
	}
	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal inbound manifest: %w", err)
	}
	payload = append(payload, '\n')
	if err := writeFileAtomic(dirs.Inbox, inboundManifestName, payload); err != nil {
		return nil, fmt.Errorf("write inbound manifest: %w", err)
	}
	return &manifest, nil
}

func CollectOutbox(ctx context.Context, staging StagingDirs, limits AttachmentLimits) ([]OutboundFile, *OutboundManifest, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	limits = normalizeLimits(limits)
	if staging.Outbox == "" {
		return nil, nil, fmt.Errorf("outbox path is empty")
	}

	entries, err := os.ReadDir(staging.Outbox)
	if err != nil {
		return nil, nil, fmt.Errorf("read outbox: %w", err)
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			return nil, nil, fmt.Errorf("unsafe outbox entry %s", entry.Name())
		}
	}
	if len(entries) > limits.MaxCount {
		return nil, nil, fmt.Errorf("attachment count %d exceeds limit %d", len(entries), limits.MaxCount)
	}

	type collected struct {
		file     OutboundFile
		artifact StagedArtifact
	}
	items := make([]collected, 0, len(entries))
	var total int64
	for _, entry := range entries {
		name, err := normalizeFilename(entry.Name())
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", entry.Name(), err)
		}
		path := filepath.Join(staging.Outbox, entry.Name())
		contents, err := readRegularFile(path)
		if err != nil {
			return nil, nil, err
		}
		size := int64(len(contents))
		if size > limits.MaxFileBytes {
			return nil, nil, fmt.Errorf("attachment size %d exceeds limit", size)
		}
		total += size
		if total > limits.MaxTotalBytes {
			return nil, nil, fmt.Errorf("attachment size %d exceeds limit", total)
		}

		contentType := storedContentType("", contents)
		sum := sha256.Sum256(contents)
		items = append(items, collected{
			file: OutboundFile{
				Filename:    name,
				ContentType: contentType,
				Contents:    contents,
			},
			artifact: StagedArtifact{
				Filename:    name,
				ContentType: contentType,
				SizeBytes:   size,
				SHA256:      hex.EncodeToString(sum[:]),
			},
		})
	}

	sort.SliceStable(items, func(i, j int) bool {
		return items[i].file.Filename < items[j].file.Filename
	})

	files := make([]OutboundFile, len(items))
	artifacts := make([]StagedArtifact, len(items))
	for index, item := range items {
		files[index] = item.file
		artifacts[index] = item.artifact
	}

	manifest := &OutboundManifest{
		TurnKey: filepath.Base(staging.Outbox),
		Files:   artifacts,
	}
	return files, manifest, nil
}

func BuildArchive(manifest OutboundManifest, files []OutboundFile) ([]byte, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("archive needs files")
	}
	if len(manifest.Files) == 0 {
		return nil, fmt.Errorf("archive manifest mismatch")
	}

	byName := make(map[string]OutboundFile, len(files))
	for _, file := range files {
		byName[file.Filename] = file
	}

	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal outbound manifest: %w", err)
	}
	payload = append(payload, '\n')

	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	if err := writeArchiveEntry(writer, outboundManifestEntry, payload); err != nil {
		_ = writer.Close()
		return nil, err
	}
	for _, artifact := range manifest.Files {
		file, ok := byName[artifact.Filename]
		if !ok {
			_ = writer.Close()
			return nil, fmt.Errorf("archive manifest mismatch")
		}
		if err := writeArchiveEntry(writer, artifact.Filename, file.Contents); err != nil {
			_ = writer.Close()
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close attachment archive: %w", err)
	}
	return buf.Bytes(), nil
}

func writeArchiveEntry(writer *zip.Writer, name string, contents []byte) error {
	header := &zip.FileHeader{
		Name:   name,
		Method: zip.Deflate,
	}
	header.Modified = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
	header.SetMode(0o644)
	entry, err := writer.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("create archive entry %s: %w", name, err)
	}
	if _, err := entry.Write(contents); err != nil {
		return fmt.Errorf("write archive entry %s: %w", name, err)
	}
	return nil
}

func VerifyAndLoadOutbox(ctx context.Context, staging StagingDirs, manifest OutboundManifest) ([]OutboundFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	files := make([]OutboundFile, 0, len(manifest.Files))
	for _, artifact := range manifest.Files {
		path := filepath.Join(staging.Outbox, artifact.Filename)
		contents, err := readRegularFile(path)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(contents)
		if int64(len(contents)) != artifact.SizeBytes || hex.EncodeToString(sum[:]) != artifact.SHA256 {
			return nil, fmt.Errorf("outbox file %s changed", artifact.Filename)
		}
		contentType := artifact.ContentType
		if contentType == "" {
			contentType = storedContentType("", contents)
		}
		files = append(files, OutboundFile{
			Filename:    artifact.Filename,
			ContentType: contentType,
			Contents:    contents,
		})
	}
	return files, nil
}

func readRegularFile(path string) ([]byte, error) {
	lstatInfo, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("lstat %s: %w", path, err)
	}
	if !lstatInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("unsafe outbox entry %s", filepath.Base(path))
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	statInfo, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	if !os.SameFile(lstatInfo, statInfo) {
		return nil, fmt.Errorf("outbox entry %s changed during read", filepath.Base(path))
	}

	contents, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return contents, nil
}

func writeFileAtomic(dir, name string, contents []byte) error {
	temp, err := os.CreateTemp(dir, ".staging-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tempName := temp.Name()
	keep := false
	defer func() {
		if !keep {
			_ = temp.Close()
			_ = os.Remove(tempName)
		}
	}()

	if err := temp.Chmod(0o600); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err := temp.Write(contents); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tempName, filepath.Join(dir, name)); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}
	keep = true
	return nil
}

func storedContentType(declared string, contents []byte) string {
	value := strings.TrimSpace(declared)
	if value == "" {
		value = http.DetectContentType(contents)
	}
	media, _, _ := strings.Cut(value, ";")
	return strings.TrimSpace(media)
}
