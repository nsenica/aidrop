package storage

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const maxSlugLength = 63

const (
	failAfterPayloadStaged      = "after-payload-staged"
	failAfterPayloadPublished   = "after-payload-published"
	failBeforeMetadataCommit    = "before-metadata-commit"
	failBeforeOldPayloadCleanup = "before-old-payload-cleanup"
	failBeforeDeleteCommit      = "before-delete-commit"
	failAfterDeleteCommit       = "after-delete-commit"
)

type Format string

const (
	FormatHTML     Format = "text/html"
	FormatMarkdown Format = "text/markdown"
	FormatZIP      Format = "application/zip"
)

var (
	ErrInvalidSlug          = errors.New("invalid slug")
	ErrInvalidSitePath      = errors.New("invalid site file path")
	ErrInvalidFormat        = errors.New("invalid artifact format")
	ErrConfirmationRequired = errors.New("project deletion confirmation is required")
	ErrConfirmationMismatch = errors.New("project deletion confirmation does not match")
	ErrConflict             = errors.New("artifact already exists")
	ErrNotFound             = errors.New("artifact not found")
	ErrCorrupt              = errors.New("artifact metadata is corrupt")
)

// Metadata is the stable, public description of an artifact.
type Metadata struct {
	Project    string    `json:"project"`
	Artifact   string    `json:"artifact"`
	Format     Format    `json:"format"`
	CreatedAt  time.Time `json:"created_at"`
	ModifiedAt time.Time `json:"modified_at"`
	PublicURL  string    `json:"url"`
	Size       int64     `json:"size"`
	EntryPoint string    `json:"entry_point,omitempty"`
}

// NewArtifact describes the source bytes to persist under a project-scoped slug.
type NewArtifact struct {
	Project    string
	Artifact   string
	Format     Format
	Source     []byte
	EntryPoint string
	SiteFiles  map[string][]byte
}

type metadataRecord struct {
	Metadata
	PayloadID string `json:"payload_id"`
}

// Repository stores artifacts beneath data/projects using JSON metadata and
// opaque payload directory names.
type Repository struct {
	projectsDir string
	catalogMu   sync.RWMutex
	locksMu     sync.Mutex
	locks       map[string]*artifactLock
	failpoint   func(string) error
}

type artifactLock struct {
	mu   sync.RWMutex
	refs int
}

// Open prepares a repository rooted at dataDir.
func Open(dataDir string) (*Repository, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, errors.New("data directory must not be empty")
	}
	projectsDir := filepath.Join(dataDir, "projects")
	if err := os.MkdirAll(projectsDir, 0o750); err != nil {
		return nil, fmt.Errorf("create projects directory: %w", err)
	}
	repo := &Repository{
		projectsDir: projectsDir,
		locks:       make(map[string]*artifactLock),
	}
	if err := repo.recover(); err != nil {
		return nil, fmt.Errorf("recover repository: %w", err)
	}
	return repo, nil
}

// ValidateSlug enforces the documented lowercase ASCII kebab-case format.
func ValidateSlug(slug string) error {
	if len(slug) < 1 || len(slug) > maxSlugLength {
		return ErrInvalidSlug
	}
	for i := 0; i < len(slug); i++ {
		c := slug[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-' && i > 0 && i < len(slug)-1 && slug[i-1] != '-':
			if slug[i+1] == '-' {
				return ErrInvalidSlug
			}
		default:
			return ErrInvalidSlug
		}
	}
	return nil
}

// ValidateSitePath accepts a relative slash-separated path inside an extracted site.
func ValidateSitePath(sitePath string) error {
	if sitePath == "." || !fs.ValidPath(sitePath) || strings.ContainsAny(sitePath, "\\:") || strings.IndexByte(sitePath, 0) >= 0 {
		return ErrInvalidSitePath
	}
	parts := strings.Split(sitePath, "/")
	if len(parts) > 16 {
		return ErrInvalidSitePath
	}
	for _, part := range parts {
		if part == "" || len(part) > 255 {
			return ErrInvalidSitePath
		}
	}
	return nil
}

// Create stores a new artifact without replacing an existing project/artifact
// pair. Metadata is published only after the source payload has been written.
func (r *Repository) Create(input NewArtifact) (Metadata, error) {
	if err := ValidateSlug(input.Project); err != nil {
		return Metadata{}, fmt.Errorf("project: %w", err)
	}
	if err := ValidateSlug(input.Artifact); err != nil {
		return Metadata{}, fmt.Errorf("artifact: %w", err)
	}
	if err := validateNewArtifact(input); err != nil {
		return Metadata{}, err
	}

	unlock := r.lockArtifact(input.Project, input.Artifact, true)
	defer unlock()

	projectDir := filepath.Join(r.projectsDir, input.Project)
	artifactDir := filepath.Join(projectDir, input.Artifact)
	if _, err := os.Lstat(artifactDir); err == nil {
		return Metadata{}, ErrConflict
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Metadata{}, fmt.Errorf("inspect artifact directory: %w", err)
	}

	projectExisted := false
	if _, err := os.Lstat(projectDir); err == nil {
		projectExisted = true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Metadata{}, fmt.Errorf("inspect project directory: %w", err)
	}
	if err := os.MkdirAll(projectDir, 0o750); err != nil {
		return Metadata{}, fmt.Errorf("create project directory: %w", err)
	}
	if err := os.Mkdir(artifactDir, 0o750); err != nil {
		if !projectExisted {
			_ = os.Remove(projectDir)
		}
		return Metadata{}, fmt.Errorf("create artifact directory: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(artifactDir)
			if !projectExisted {
				_ = os.Remove(projectDir)
			}
		}
	}()

	payloadID, payloadDir, err := r.writePayload(artifactDir, input)
	if err != nil {
		return Metadata{}, err
	}

	now := time.Now().UTC()
	metadata := Metadata{
		Project:    input.Project,
		Artifact:   input.Artifact,
		Format:     input.Format,
		CreatedAt:  now,
		ModifiedAt: now,
		PublicURL:  publicURL(input.Project, input.Artifact),
		Size:       int64(len(input.Source)),
		EntryPoint: input.EntryPoint,
	}
	record := metadataRecord{Metadata: metadata, PayloadID: payloadID}
	encoded, err := json.Marshal(record)
	if err != nil {
		_ = os.RemoveAll(payloadDir)
		return Metadata{}, fmt.Errorf("encode artifact metadata: %w", err)
	}
	metadataCommitted, err := r.writeAtomicFile(artifactDir, "metadata.json", encoded, 0o640)
	if !metadataCommitted {
		_ = os.RemoveAll(payloadDir)
		return Metadata{}, fmt.Errorf("publish artifact metadata: %w", err)
	}
	committed = true
	return metadata, nil
}

// Get returns metadata and the exact uploaded source bytes.
func (r *Repository) Get(project, artifact string) (Metadata, []byte, error) {
	if err := ValidateSlug(project); err != nil {
		return Metadata{}, nil, fmt.Errorf("project: %w", err)
	}
	if err := ValidateSlug(artifact); err != nil {
		return Metadata{}, nil, fmt.Errorf("artifact: %w", err)
	}
	unlock := r.lockArtifact(project, artifact, false)
	defer unlock()

	record, err := r.readRecord(project, artifact)
	if err != nil {
		return Metadata{}, nil, err
	}
	sourcePath := filepath.Join(r.payloadDir(project, artifact, record.PayloadID), "source")
	source, err := os.ReadFile(sourcePath)
	if errors.Is(err, fs.ErrNotExist) {
		return Metadata{}, nil, fmt.Errorf("%w: source payload is missing", ErrCorrupt)
	}
	if err != nil {
		return Metadata{}, nil, fmt.Errorf("read source payload: %w", err)
	}
	if int64(len(source)) != record.Size {
		return Metadata{}, nil, fmt.Errorf("%w: source size does not match metadata", ErrCorrupt)
	}
	return record.Metadata, source, nil
}

// GetMetadata returns persisted metadata without reading the source payload.
func (r *Repository) GetMetadata(project, artifact string) (Metadata, error) {
	if err := ValidateSlug(project); err != nil {
		return Metadata{}, fmt.Errorf("project: %w", err)
	}
	if err := ValidateSlug(artifact); err != nil {
		return Metadata{}, fmt.Errorf("artifact: %w", err)
	}
	unlock := r.lockArtifact(project, artifact, false)
	defer unlock()

	record, err := r.readRecord(project, artifact)
	if err != nil {
		return Metadata{}, err
	}
	return record.Metadata, nil
}

// GetPublicRoot returns one complete body for the artifact's stable public URL.
func (r *Repository) GetPublicRoot(project, artifact string) (Metadata, []byte, error) {
	if err := ValidateSlug(project); err != nil {
		return Metadata{}, nil, fmt.Errorf("project: %w", err)
	}
	if err := ValidateSlug(artifact); err != nil {
		return Metadata{}, nil, fmt.Errorf("artifact: %w", err)
	}
	unlock := r.lockArtifact(project, artifact, false)
	defer unlock()

	record, err := r.readRecord(project, artifact)
	if err != nil {
		return Metadata{}, nil, err
	}
	if record.Format == FormatZIP {
		contents, err := r.readSiteFileLocked(project, artifact, record, record.EntryPoint)
		return record.Metadata, contents, err
	}
	contents, err := r.readSourceLocked(project, artifact, record)
	return record.Metadata, contents, err
}

// GetSiteFile returns a regular file from the committed extracted ZIP site.
func (r *Repository) GetSiteFile(project, artifact, sitePath string) ([]byte, error) {
	if err := ValidateSlug(project); err != nil {
		return nil, fmt.Errorf("project: %w", err)
	}
	if err := ValidateSlug(artifact); err != nil {
		return nil, fmt.Errorf("artifact: %w", err)
	}
	if err := ValidateSitePath(sitePath); err != nil {
		return nil, err
	}
	unlock := r.lockArtifact(project, artifact, false)
	defer unlock()

	record, err := r.readRecord(project, artifact)
	if err != nil {
		return nil, err
	}
	if record.Format != FormatZIP {
		return nil, fmt.Errorf("%w: artifact is not a ZIP site", ErrNotFound)
	}
	return r.readSiteFileLocked(project, artifact, record, sitePath)
}

func (r *Repository) readSourceLocked(project, artifact string, record metadataRecord) ([]byte, error) {
	sourcePath := filepath.Join(r.payloadDir(project, artifact, record.PayloadID), "source")
	source, err := os.ReadFile(sourcePath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: source payload is missing", ErrCorrupt)
	}
	if err != nil {
		return nil, fmt.Errorf("read source payload: %w", err)
	}
	if int64(len(source)) != record.Size {
		return nil, fmt.Errorf("%w: source size does not match metadata", ErrCorrupt)
	}
	return source, nil
}

func (r *Repository) readSiteFileLocked(project, artifact string, record metadataRecord, sitePath string) ([]byte, error) {
	current := filepath.Join(r.payloadDir(project, artifact, record.PayloadID), "site")
	parts := strings.Split(sitePath, "/")
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: site file %q", ErrNotFound, sitePath)
		}
		if err != nil {
			return nil, fmt.Errorf("inspect site file: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%w: site path contains a symlink", ErrCorrupt)
		}
		if i < len(parts)-1 && !info.IsDir() {
			return nil, fmt.Errorf("%w: site file %q", ErrNotFound, sitePath)
		}
		if i == len(parts)-1 && !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%w: site file %q", ErrNotFound, sitePath)
		}
	}
	contents, err := os.ReadFile(current)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: site file %q", ErrNotFound, sitePath)
	}
	if err != nil {
		return nil, fmt.Errorf("read site file: %w", err)
	}
	return contents, nil
}

// ListProjects returns the valid project directories in stable slug order.
func (r *Repository) ListProjects() ([]string, error) {
	r.catalogMu.Lock()
	defer r.catalogMu.Unlock()

	entries, err := os.ReadDir(r.projectsDir)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	projects := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || ValidateSlug(entry.Name()) != nil {
			continue
		}
		projects = append(projects, entry.Name())
	}
	sort.Strings(projects)
	return projects, nil
}

// DeleteProject removes a project namespace after the caller confirms its exact slug.
// It takes the catalog lock exclusively so no artifact operation can overlap the
// namespace rename or cleanup.
func (r *Repository) DeleteProject(project, confirmation string) error {
	if err := ValidateSlug(project); err != nil {
		return fmt.Errorf("project: %w", err)
	}
	if confirmation == "" {
		return ErrConfirmationRequired
	}
	if confirmation != project {
		return ErrConfirmationMismatch
	}

	r.catalogMu.Lock()
	defer r.catalogMu.Unlock()

	projectDir := filepath.Join(r.projectsDir, project)
	info, err := os.Lstat(projectDir)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: project %q", ErrNotFound, project)
	}
	if err != nil {
		return fmt.Errorf("inspect project directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: project path is not a directory", ErrCorrupt)
	}

	deletionID, err := newPayloadID()
	if err != nil {
		return fmt.Errorf("create project deletion identifier: %w", err)
	}
	tombstone := filepath.Join(r.projectsDir, ".delete-project-"+project+"-"+deletionID)
	if err := os.Rename(projectDir, tombstone); err != nil {
		return fmt.Errorf("commit project deletion: %w", err)
	}
	if err := syncDirectory(r.projectsDir); err != nil {
		if rollbackErr := os.Rename(tombstone, projectDir); rollbackErr == nil {
			_ = syncDirectory(r.projectsDir)
			return fmt.Errorf("sync project deletion: %w", err)
		}
		return fmt.Errorf("sync committed project deletion: %w", err)
	}
	// The rename commits deletion from the live namespace. Cleanup is best-effort;
	// startup recovery removes a tombstone if filesystem cleanup is interrupted.
	if err := os.RemoveAll(tombstone); err == nil {
		_ = syncDirectory(r.projectsDir)
	}
	return nil
}

// ListArtifacts returns the persisted artifacts for one existing project.
func (r *Repository) ListArtifacts(project string) ([]Metadata, error) {
	if err := ValidateSlug(project); err != nil {
		return nil, fmt.Errorf("project: %w", err)
	}
	r.catalogMu.Lock()
	defer r.catalogMu.Unlock()
	return r.listArtifacts(project)
}

func (r *Repository) listArtifacts(project string) ([]Metadata, error) {
	projectDir := filepath.Join(r.projectsDir, project)
	entries, err := os.ReadDir(projectDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: project %q", ErrNotFound, project)
	}
	if err != nil {
		return nil, fmt.Errorf("list project artifacts: %w", err)
	}

	artifacts := make([]Metadata, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || ValidateSlug(entry.Name()) != nil {
			continue
		}
		record, err := r.readRecord(project, entry.Name())
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, record.Metadata)
	}
	sort.Slice(artifacts, func(i, j int) bool {
		return artifacts[i].Artifact < artifacts[j].Artifact
	})
	return artifacts, nil
}

// DeleteArtifact removes an artifact while leaving its project available.
func (r *Repository) DeleteArtifact(project, artifact string) error {
	if err := ValidateSlug(project); err != nil {
		return fmt.Errorf("project: %w", err)
	}
	if err := ValidateSlug(artifact); err != nil {
		return fmt.Errorf("artifact: %w", err)
	}
	unlock := r.lockArtifact(project, artifact, true)
	defer unlock()

	if _, err := r.readRecord(project, artifact); err != nil {
		return err
	}
	projectDir := filepath.Join(r.projectsDir, project)
	artifactDir := filepath.Join(projectDir, artifact)
	tombstoneID, err := newPayloadID()
	if err != nil {
		return fmt.Errorf("create deletion identifier: %w", err)
	}
	if err := r.injectFailure(failBeforeDeleteCommit); err != nil {
		return err
	}
	tombstone := filepath.Join(projectDir, ".delete-"+tombstoneID)
	if err := os.Rename(artifactDir, tombstone); err != nil {
		return fmt.Errorf("commit artifact deletion: %w", err)
	}
	_ = syncDirectory(projectDir)
	if err := r.injectFailure(failAfterDeleteCommit); err == nil {
		_ = os.RemoveAll(tombstone)
	}
	return nil
}

// Replace atomically switches an existing artifact to a complete new payload.
func (r *Repository) Replace(input NewArtifact) (Metadata, error) {
	if err := ValidateSlug(input.Project); err != nil {
		return Metadata{}, fmt.Errorf("project: %w", err)
	}
	if err := ValidateSlug(input.Artifact); err != nil {
		return Metadata{}, fmt.Errorf("artifact: %w", err)
	}
	if err := validateNewArtifact(input); err != nil {
		return Metadata{}, err
	}

	unlock := r.lockArtifact(input.Project, input.Artifact, true)
	defer unlock()

	current, err := r.readRecord(input.Project, input.Artifact)
	if err != nil {
		return Metadata{}, err
	}
	artifactDir := filepath.Join(r.projectsDir, input.Project, input.Artifact)
	newPayloadID, newPayloadDir, err := r.writePayload(artifactDir, input)
	if err != nil {
		return Metadata{}, err
	}
	newPayloadCommitted := false
	defer func() {
		if !newPayloadCommitted {
			_ = os.RemoveAll(newPayloadDir)
		}
	}()

	now := time.Now().UTC()
	if !now.After(current.ModifiedAt) {
		now = current.ModifiedAt.Add(time.Nanosecond)
	}
	metadata := current.Metadata
	metadata.Format = input.Format
	metadata.ModifiedAt = now
	metadata.Size = int64(len(input.Source))
	metadata.EntryPoint = input.EntryPoint
	record := metadataRecord{Metadata: metadata, PayloadID: newPayloadID}
	encoded, err := json.Marshal(record)
	if err != nil {
		return Metadata{}, fmt.Errorf("encode artifact metadata: %w", err)
	}

	committed, err := r.writeAtomicFile(artifactDir, "metadata.json", encoded, 0o640)
	if !committed {
		return Metadata{}, fmt.Errorf("replace artifact metadata: %w", err)
	}
	newPayloadCommitted = true
	if err != nil {
		return metadata, nil
	}
	if err := r.injectFailure(failBeforeOldPayloadCleanup); err != nil {
		return metadata, nil
	}
	oldPayloadDir := r.payloadDir(input.Project, input.Artifact, current.PayloadID)
	if err := os.RemoveAll(oldPayloadDir); err == nil {
		_ = syncDirectory(filepath.Join(artifactDir, "payloads"))
	}
	return metadata, nil
}

func (r *Repository) readRecord(project, artifact string) (metadataRecord, error) {
	metadataPath := filepath.Join(r.projectsDir, project, artifact, "metadata.json")
	contents, err := os.ReadFile(metadataPath)
	if errors.Is(err, fs.ErrNotExist) {
		return metadataRecord{}, fmt.Errorf("%w: %s/%s", ErrNotFound, project, artifact)
	}
	if err != nil {
		return metadataRecord{}, fmt.Errorf("read artifact metadata: %w", err)
	}
	var record metadataRecord
	if err := json.Unmarshal(contents, &record); err != nil {
		return metadataRecord{}, fmt.Errorf("%w: decode metadata: %v", ErrCorrupt, err)
	}
	if record.Project != project || record.Artifact != artifact || !validFormat(record.Format) ||
		record.Size < 0 || record.CreatedAt.IsZero() || record.ModifiedAt.IsZero() ||
		record.PublicURL != publicURL(project, artifact) || !validPayloadID(record.PayloadID) {
		return metadataRecord{}, fmt.Errorf("%w: invalid fields for %s/%s", ErrCorrupt, project, artifact)
	}
	if record.Format == FormatZIP {
		if ValidateSitePath(record.EntryPoint) != nil {
			return metadataRecord{}, fmt.Errorf("%w: invalid site entry point for %s/%s", ErrCorrupt, project, artifact)
		}
	} else if record.EntryPoint != "" {
		return metadataRecord{}, fmt.Errorf("%w: unexpected site entry point for %s/%s", ErrCorrupt, project, artifact)
	}
	return record, nil
}

func (r *Repository) payloadDir(project, artifact, payloadID string) string {
	return filepath.Join(r.projectsDir, project, artifact, "payloads", payloadID)
}

func (r *Repository) lockArtifact(project, artifact string, write bool) func() {
	r.catalogMu.RLock()
	key := project + "\x00" + artifact
	r.locksMu.Lock()
	lock := r.locks[key]
	if lock == nil {
		lock = &artifactLock{}
		r.locks[key] = lock
	}
	lock.refs++
	r.locksMu.Unlock()

	if write {
		lock.mu.Lock()
	} else {
		lock.mu.RLock()
	}
	return func() {
		if write {
			lock.mu.Unlock()
		} else {
			lock.mu.RUnlock()
		}
		r.locksMu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(r.locks, key)
		}
		r.locksMu.Unlock()
		r.catalogMu.RUnlock()
	}
}

func (r *Repository) writePayload(artifactDir string, input NewArtifact) (string, string, error) {
	payloadsDir := filepath.Join(artifactDir, "payloads")
	if err := os.MkdirAll(payloadsDir, 0o750); err != nil {
		return "", "", fmt.Errorf("create payload directory: %w", err)
	}
	payloadID, err := newPayloadID()
	if err != nil {
		return "", "", fmt.Errorf("create payload identifier: %w", err)
	}
	stageDir, err := os.MkdirTemp(payloadsDir, ".stage-")
	if err != nil {
		return "", "", fmt.Errorf("create payload staging directory: %w", err)
	}
	defer os.RemoveAll(stageDir)
	if err := writeSyncedFile(filepath.Join(stageDir, "source"), input.Source, 0o640); err != nil {
		return "", "", fmt.Errorf("write source payload: %w", err)
	}
	if len(input.SiteFiles) > 0 {
		if err := writeSiteFiles(filepath.Join(stageDir, "site"), input.SiteFiles); err != nil {
			return "", "", fmt.Errorf("write extracted site: %w", err)
		}
	}
	if err := syncDirectory(stageDir); err != nil {
		return "", "", fmt.Errorf("sync staged payload: %w", err)
	}
	if err := r.injectFailure(failAfterPayloadStaged); err != nil {
		return "", "", err
	}
	payloadDir := filepath.Join(payloadsDir, payloadID)
	if err := os.Rename(stageDir, payloadDir); err != nil {
		return "", "", fmt.Errorf("publish source payload: %w", err)
	}
	if err := syncDirectory(payloadsDir); err != nil {
		_ = os.RemoveAll(payloadDir)
		return "", "", fmt.Errorf("sync payload directory: %w", err)
	}
	if err := r.injectFailure(failAfterPayloadPublished); err != nil {
		_ = os.RemoveAll(payloadDir)
		return "", "", err
	}
	return payloadID, payloadDir, nil
}

func writeSiteFiles(siteRoot string, files map[string][]byte) error {
	if err := os.MkdirAll(siteRoot, 0o750); err != nil {
		return err
	}
	for sitePath, contents := range files {
		if err := ValidateSitePath(sitePath); err != nil {
			return err
		}
		destination := filepath.Join(siteRoot, filepath.FromSlash(sitePath))
		if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
			return err
		}
		if err := writeSyncedFile(destination, contents, 0o640); err != nil {
			return err
		}
	}
	var directories []string
	if err := filepath.WalkDir(siteRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			directories = append(directories, path)
		}
		return nil
	}); err != nil {
		return err
	}
	sort.Slice(directories, func(i, j int) bool {
		return len(directories[i]) > len(directories[j])
	})
	for _, directory := range directories {
		if err := syncDirectory(directory); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) injectFailure(point string) error {
	if r.failpoint == nil {
		return nil
	}
	return r.failpoint(point)
}

func (r *Repository) recover() error {
	projects, err := os.ReadDir(r.projectsDir)
	if err != nil {
		return fmt.Errorf("list projects during recovery: %w", err)
	}
	for _, projectEntry := range projects {
		if strings.HasPrefix(projectEntry.Name(), ".delete-project-") {
			if err := os.RemoveAll(filepath.Join(r.projectsDir, projectEntry.Name())); err != nil {
				return fmt.Errorf("remove project deletion tombstone: %w", err)
			}
			continue
		}
		if !projectEntry.IsDir() || ValidateSlug(projectEntry.Name()) != nil {
			continue
		}
		projectDir := filepath.Join(r.projectsDir, projectEntry.Name())
		artifacts, err := os.ReadDir(projectDir)
		if err != nil {
			return fmt.Errorf("list project %q during recovery: %w", projectEntry.Name(), err)
		}
		for _, artifactEntry := range artifacts {
			artifactDir := filepath.Join(projectDir, artifactEntry.Name())
			if strings.HasPrefix(artifactEntry.Name(), ".delete-") {
				if err := os.RemoveAll(artifactDir); err != nil {
					return fmt.Errorf("remove deletion tombstone: %w", err)
				}
				continue
			}
			if !artifactEntry.IsDir() || ValidateSlug(artifactEntry.Name()) != nil {
				continue
			}
			metadataPath := filepath.Join(artifactDir, "metadata.json")
			if _, err := os.Lstat(metadataPath); errors.Is(err, fs.ErrNotExist) {
				if err := os.RemoveAll(artifactDir); err != nil {
					return fmt.Errorf("remove uncommitted artifact: %w", err)
				}
				continue
			} else if err != nil {
				return fmt.Errorf("inspect artifact metadata during recovery: %w", err)
			}
			record, err := r.readRecord(projectEntry.Name(), artifactEntry.Name())
			if err != nil {
				return fmt.Errorf("validate %s/%s during recovery: %w", projectEntry.Name(), artifactEntry.Name(), err)
			}
			payloadsDir := filepath.Join(artifactDir, "payloads")
			payloads, err := os.ReadDir(payloadsDir)
			if err != nil {
				return fmt.Errorf("list payloads for %s/%s: %w", projectEntry.Name(), artifactEntry.Name(), err)
			}
			if err := validateCommittedPayload(r.payloadDir(projectEntry.Name(), artifactEntry.Name(), record.PayloadID), record); err != nil {
				return fmt.Errorf("validate committed payload for %s/%s: %w", projectEntry.Name(), artifactEntry.Name(), err)
			}
			for _, payloadEntry := range payloads {
				if payloadEntry.Name() == record.PayloadID {
					continue
				}
				if err := os.RemoveAll(filepath.Join(payloadsDir, payloadEntry.Name())); err != nil {
					return fmt.Errorf("remove unreferenced payload: %w", err)
				}
			}
			if err := removeMetadataTemps(artifactDir); err != nil {
				return fmt.Errorf("remove stale metadata temp for %s/%s: %w", projectEntry.Name(), artifactEntry.Name(), err)
			}
		}
	}
	return nil
}

func validateCommittedPayload(payloadDir string, record metadataRecord) error {
	payloadInfo, err := os.Lstat(payloadDir)
	if err != nil {
		return fmt.Errorf("%w: payload directory is missing", ErrCorrupt)
	}
	if !payloadInfo.IsDir() || payloadInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: payload path is not a directory", ErrCorrupt)
	}
	sourcePath := filepath.Join(payloadDir, "source")
	sourceInfo, err := os.Lstat(sourcePath)
	if err != nil {
		return fmt.Errorf("%w: source file is missing", ErrCorrupt)
	}
	if !sourceInfo.Mode().IsRegular() || sourceInfo.Size() != record.Size {
		return fmt.Errorf("%w: source file does not match metadata", ErrCorrupt)
	}
	if record.Format == FormatZIP {
		if err := validateSiteTree(filepath.Join(payloadDir, "site"), record.EntryPoint); err != nil {
			return err
		}
	}
	return nil
}

func validateSiteTree(siteRoot, entryPoint string) error {
	rootInfo, err := os.Lstat(siteRoot)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: extracted site directory is missing or unsafe", ErrCorrupt)
	}
	var totalSize int64
	fileCount := 0
	entryPointFound := false
	err = filepath.WalkDir(siteRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == siteRoot {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: extracted site contains a symlink", ErrCorrupt)
		}
		relative, err := filepath.Rel(siteRoot, path)
		if err != nil {
			return err
		}
		sitePath := filepath.ToSlash(relative)
		if err := ValidateSitePath(sitePath); err != nil {
			return fmt.Errorf("%w: extracted site contains an invalid path", ErrCorrupt)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%w: extracted site contains a non-regular file", ErrCorrupt)
		}
		fileCount++
		totalSize += info.Size()
		if fileCount > 1000 || totalSize > 100<<20 {
			return fmt.Errorf("%w: extracted site exceeds storage limits", ErrCorrupt)
		}
		if sitePath == entryPoint {
			entryPointFound = true
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !entryPointFound {
		return fmt.Errorf("%w: extracted site entry point is missing", ErrCorrupt)
	}
	return nil
}

func removeMetadataTemps(artifactDir string) error {
	entries, err := os.ReadDir(artifactDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".metadata-") {
			if err := os.RemoveAll(filepath.Join(artifactDir, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateNewArtifact(input NewArtifact) error {
	if !validFormat(input.Format) {
		return ErrInvalidFormat
	}
	if input.Format != FormatZIP {
		if input.EntryPoint != "" || len(input.SiteFiles) != 0 {
			return ErrInvalidFormat
		}
		return nil
	}
	if ValidateSitePath(input.EntryPoint) != nil {
		return ErrInvalidSitePath
	}
	if len(input.SiteFiles) == 0 {
		return fmt.Errorf("%w: ZIP site has no files", ErrInvalidFormat)
	}
	if _, ok := input.SiteFiles[input.EntryPoint]; !ok {
		return fmt.Errorf("%w: ZIP entry point is missing", ErrInvalidFormat)
	}
	for sitePath := range input.SiteFiles {
		if err := ValidateSitePath(sitePath); err != nil {
			return err
		}
	}
	return nil
}

func validFormat(format Format) bool {
	switch format {
	case FormatHTML, FormatMarkdown, FormatZIP:
		return true
	default:
		return false
	}
}

func publicURL(project, artifact string) string {
	return "/p/" + project + "/" + artifact + "/"
}

func newPayloadID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func validPayloadID(payloadID string) bool {
	if len(payloadID) != 32 {
		return false
	}
	_, err := hex.DecodeString(payloadID)
	return err == nil
}

func writeSyncedFile(path string, contents []byte, mode fs.FileMode) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if n, err := file.Write(contents); err != nil {
		_ = file.Close()
		return err
	} else if n != len(contents) {
		_ = file.Close()
		return fmt.Errorf("short write: wrote %d of %d bytes", n, len(contents))
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func (r *Repository) writeAtomicFile(dir, name string, contents []byte, mode fs.FileMode) (bool, error) {
	temp, err := os.CreateTemp(dir, ".metadata-")
	if err != nil {
		return false, err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return false, err
	}
	if n, err := temp.Write(contents); err != nil {
		_ = temp.Close()
		return false, err
	} else if n != len(contents) {
		_ = temp.Close()
		return false, fmt.Errorf("short write: wrote %d of %d bytes", n, len(contents))
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return false, err
	}
	if err := temp.Close(); err != nil {
		return false, err
	}
	if err := r.injectFailure(failBeforeMetadataCommit); err != nil {
		return false, err
	}
	if err := os.Rename(tempPath, filepath.Join(dir, name)); err != nil {
		return false, err
	}
	return true, syncDirectory(dir)
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
