package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestValidateSlug(t *testing.T) {
	valid := []string{"a", "project-1", "one-two-3", strings.Repeat("a", maxSlugLength)}
	for _, slug := range valid {
		t.Run("valid_"+slug, func(t *testing.T) {
			if err := ValidateSlug(slug); err != nil {
				t.Errorf("ValidateSlug(%q) = %v, want nil", slug, err)
			}
		})
	}

	invalid := []string{"", "A", "Upper", "a--b", "-a", "a-", "a.b", "a/b", "a b", "é", strings.Repeat("a", maxSlugLength+1)}
	for _, slug := range invalid {
		t.Run("invalid_"+slug, func(t *testing.T) {
			if err := ValidateSlug(slug); !errors.Is(err, ErrInvalidSlug) {
				t.Errorf("ValidateSlug(%q) = %v, want ErrInvalidSlug", slug, err)
			}
		})
	}
}

func TestCreateIsProjectScopedAndPreservesExactSource(t *testing.T) {
	repo, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	source := []byte("<!doctype html>\x00\r\nexact source\n")
	first, err := repo.Create(NewArtifact{
		Project:  "alpha",
		Artifact: "site",
		Format:   FormatHTML,
		Source:   source,
	})
	if err != nil {
		t.Fatalf("create alpha/site: %v", err)
	}
	if first.Project != "alpha" || first.Artifact != "site" || first.Format != FormatHTML {
		t.Fatalf("unexpected metadata: %+v", first)
	}
	if first.CreatedAt.IsZero() || first.ModifiedAt.IsZero() || first.ModifiedAt.Before(first.CreatedAt) {
		t.Fatalf("invalid timestamps: %+v", first)
	}
	if first.PublicURL != "/p/alpha/site/" {
		t.Errorf("public URL = %q, want /p/alpha/site/", first.PublicURL)
	}
	if first.Size != int64(len(source)) {
		t.Errorf("size = %d, want %d", first.Size, len(source))
	}

	if _, err := repo.Create(NewArtifact{Project: "alpha", Artifact: "site", Format: FormatHTML, Source: []byte("duplicate")}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate create error = %v, want ErrConflict", err)
	}
	if _, err := repo.Create(NewArtifact{Project: "beta", Artifact: "site", Format: FormatHTML, Source: []byte("other project")}); err != nil {
		t.Fatalf("same artifact slug in another project: %v", err)
	}

	gotMetadata, gotSource, err := repo.Get("alpha", "site")
	if err != nil {
		t.Fatalf("get alpha/site: %v", err)
	}
	if gotMetadata != first {
		t.Errorf("retrieved metadata = %+v, want %+v", gotMetadata, first)
	}
	if !bytes.Equal(gotSource, source) {
		t.Errorf("retrieved source = %q, want exact %q", gotSource, source)
	}
}

func TestInvalidOrUnsupportedPublishCreatesNoVisibleProject(t *testing.T) {
	repo, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	tests := []NewArtifact{
		{Project: "BadProject", Artifact: "site", Format: FormatHTML, Source: []byte("body")},
		{Project: "valid", Artifact: "BadArtifact", Format: FormatHTML, Source: []byte("body")},
		{Project: "valid", Artifact: "site", Format: Format("text/plain"), Source: []byte("body")},
	}
	for _, input := range tests {
		if _, err := repo.Create(input); err == nil {
			t.Errorf("Create(%+v) succeeded, want validation error", input)
		}
	}
	projects, err := repo.ListProjects()
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(projects) != 0 {
		t.Errorf("visible projects = %v, want none", projects)
	}
}

func TestListsPersistAfterRepositoryReopen(t *testing.T) {
	dataDir := t.TempDir()
	repo, err := Open(dataDir)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	for _, input := range []NewArtifact{
		{Project: "zeta", Artifact: "last", Format: FormatMarkdown, Source: []byte("# Last")},
		{Project: "alpha", Artifact: "second", Format: FormatMarkdown, Source: []byte("# Second")},
		{Project: "alpha", Artifact: "first", Format: FormatHTML, Source: []byte("<h1>First</h1>")},
	} {
		if _, err := repo.Create(input); err != nil {
			t.Fatalf("create %s/%s: %v", input.Project, input.Artifact, err)
		}
	}

	reopened, err := Open(dataDir)
	if err != nil {
		t.Fatalf("reopen repository: %v", err)
	}
	projects, err := reopened.ListProjects()
	if err != nil {
		t.Fatalf("list projects after reopen: %v", err)
	}
	if want := []string{"alpha", "zeta"}; !equalStrings(projects, want) {
		t.Fatalf("projects = %v, want %v", projects, want)
	}
	artifacts, err := reopened.ListArtifacts("alpha")
	if err != nil {
		t.Fatalf("list alpha artifacts after reopen: %v", err)
	}
	if len(artifacts) != 2 || artifacts[0].Artifact != "first" || artifacts[1].Artifact != "second" {
		t.Fatalf("alpha artifacts = %+v, want first then second", artifacts)
	}
	if _, _, err := reopened.Get("alpha", "first"); err != nil {
		t.Errorf("get persisted artifact after reopen: %v", err)
	}
}

func TestDeleteArtifactLeavesEmptyProject(t *testing.T) {
	repo, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	if _, err := repo.Create(NewArtifact{Project: "alpha", Artifact: "site", Format: FormatHTML, Source: []byte("body")}); err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	if err := repo.DeleteArtifact("alpha", "site"); err != nil {
		t.Fatalf("delete artifact: %v", err)
	}
	if _, _, err := repo.Get("alpha", "site"); !errors.Is(err, ErrNotFound) {
		t.Errorf("get deleted artifact error = %v, want ErrNotFound", err)
	}
	projects, err := repo.ListProjects()
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if !equalStrings(projects, []string{"alpha"}) {
		t.Errorf("projects after deleting last artifact = %v, want [alpha]", projects)
	}
	artifacts, err := repo.ListArtifacts("alpha")
	if err != nil {
		t.Fatalf("list empty project: %v", err)
	}
	if len(artifacts) != 0 {
		t.Errorf("artifacts after delete = %+v, want none", artifacts)
	}
}

func TestDeleteProjectRequiresExactConfirmationAndAllowsRecreation(t *testing.T) {
	repo, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	for _, input := range []NewArtifact{
		{Project: "alpha", Artifact: "report", Format: FormatHTML, Source: []byte("alpha report")},
		{Project: "alpha", Artifact: "notes", Format: FormatMarkdown, Source: []byte("# alpha notes")},
		{Project: "beta", Artifact: "report", Format: FormatHTML, Source: []byte("beta report")},
	} {
		if _, err := repo.Create(input); err != nil {
			t.Fatalf("create %s/%s: %v", input.Project, input.Artifact, err)
		}
	}
	if err := repo.DeleteProject("alpha", ""); !errors.Is(err, ErrConfirmationRequired) {
		t.Fatalf("missing confirmation error = %v, want ErrConfirmationRequired", err)
	}
	if err := repo.DeleteProject("alpha", "beta"); !errors.Is(err, ErrConfirmationMismatch) {
		t.Fatalf("mismatched confirmation error = %v, want ErrConfirmationMismatch", err)
	}
	if _, _, err := repo.Get("alpha", "report"); err != nil {
		t.Fatalf("rejected deletion changed project data: %v", err)
	}

	if err := repo.DeleteProject("alpha", "alpha"); err != nil {
		t.Fatalf("delete confirmed project: %v", err)
	}
	if _, _, err := repo.Get("alpha", "report"); !errors.Is(err, ErrNotFound) {
		t.Errorf("get deleted alpha artifact error=%v, want ErrNotFound", err)
	}
	if _, err := repo.ListArtifacts("alpha"); !errors.Is(err, ErrNotFound) {
		t.Errorf("list deleted alpha artifacts error=%v, want ErrNotFound", err)
	}
	if _, source, err := repo.Get("beta", "report"); err != nil || string(source) != "beta report" {
		t.Errorf("unrelated project after deletion source=%q err=%v", source, err)
	}
	if _, err := repo.Create(NewArtifact{Project: "alpha", Artifact: "report", Format: FormatHTML, Source: []byte("recreated")}); err != nil {
		t.Fatalf("recreate deleted project slug: %v", err)
	}
	projects, err := repo.ListProjects()
	if err != nil {
		t.Fatalf("list projects after recreation: %v", err)
	}
	if want := []string{"alpha", "beta"}; !equalStrings(projects, want) {
		t.Errorf("projects after recreation=%v, want %v", projects, want)
	}
}

func TestDeleteProjectSerializesWithConcurrentPublishAndUpdate(t *testing.T) {
	repo, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	if _, err := repo.Create(NewArtifact{Project: "alpha", Artifact: "base", Format: FormatHTML, Source: []byte("base")}); err != nil {
		t.Fatalf("create base artifact: %v", err)
	}

	start := make(chan struct{})
	var wait sync.WaitGroup
	errorsSeen := make(chan error, 10)
	createdSource := make(map[string][]byte)
	for i := 0; i < 8; i++ {
		artifact := fmt.Sprintf("new-%d", i)
		source := []byte("complete payload " + artifact)
		createdSource[artifact] = source
		wait.Add(1)
		go func(artifact string, source []byte) {
			defer wait.Done()
			<-start
			_, err := repo.Create(NewArtifact{Project: "alpha", Artifact: artifact, Format: FormatHTML, Source: source})
			errorsSeen <- err
		}(artifact, source)
	}
	wait.Add(1)
	go func() {
		defer wait.Done()
		<-start
		_, err := repo.Replace(NewArtifact{Project: "alpha", Artifact: "base", Format: FormatHTML, Source: []byte("updated base")})
		if err != nil && !errors.Is(err, ErrNotFound) {
			errorsSeen <- err
			return
		}
		errorsSeen <- nil
	}()
	wait.Add(1)
	go func() {
		defer wait.Done()
		<-start
		errorsSeen <- repo.DeleteProject("alpha", "alpha")
	}()
	close(start)
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Errorf("concurrent operation failed: %v", err)
		}
	}

	projects, err := repo.ListProjects()
	if err != nil {
		t.Fatalf("list projects after concurrent operations: %v", err)
	}
	if len(projects) == 0 {
		return
	}
	if !equalStrings(projects, []string{"alpha"}) {
		t.Fatalf("projects after concurrent operations=%v, want empty or [alpha]", projects)
	}
	artifacts, err := repo.ListArtifacts("alpha")
	if err != nil {
		t.Fatalf("list surviving project artifacts: %v", err)
	}
	if len(artifacts) == 0 {
		t.Fatal("project directory is visible without a committed artifact")
	}
	for _, metadata := range artifacts {
		want, ok := createdSource[metadata.Artifact]
		if !ok {
			t.Fatalf("unexpected artifact survived deletion: %q", metadata.Artifact)
		}
		_, source, err := repo.Get("alpha", metadata.Artifact)
		if err != nil || !bytes.Equal(source, want) {
			t.Errorf("surviving artifact %q source=%q err=%v, want complete payload", metadata.Artifact, source, err)
		}
	}
}

func TestOpenCleansInterruptedProjectDeletion(t *testing.T) {
	dataDir := t.TempDir()
	repo, err := Open(dataDir)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	if _, err := repo.Create(NewArtifact{Project: "alpha", Artifact: "report", Format: FormatHTML, Source: []byte("report")}); err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	projectDir := filepath.Join(dataDir, "projects", "alpha")
	tombstone := filepath.Join(dataDir, "projects", ".delete-project-alpha-"+strings.Repeat("a", 32))
	if err := os.Rename(projectDir, tombstone); err != nil {
		t.Fatalf("stage project deletion tombstone: %v", err)
	}

	reopened, err := Open(dataDir)
	if err != nil {
		t.Fatalf("reopen repository with interrupted deletion: %v", err)
	}
	if _, err := os.Lstat(tombstone); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("project deletion tombstone remains after recovery: %v", err)
	}
	if projects, err := reopened.ListProjects(); err != nil || len(projects) != 0 {
		t.Errorf("projects after deletion recovery=%v err=%v, want empty", projects, err)
	}
}

func TestZIPPayloadPersistsOriginalAndExtractedFiles(t *testing.T) {
	dataDir := t.TempDir()
	repo, err := Open(dataDir)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	original := []byte("original ZIP bytes")
	files := map[string][]byte{
		"index.html":         []byte("<h1>site</h1>"),
		"assets/css/app.css": []byte("body { color: blue }"),
		"nested/app.js":      []byte("console.log('site')"),
	}
	if _, err := repo.Create(NewArtifact{
		Project:    "alpha",
		Artifact:   "site",
		Format:     FormatZIP,
		Source:     original,
		EntryPoint: "index.html",
		SiteFiles:  files,
	}); err != nil {
		t.Fatalf("create ZIP artifact: %v", err)
	}
	_, source, err := repo.Get("alpha", "site")
	if err != nil || !bytes.Equal(source, original) {
		t.Fatalf("source = %q, err=%v; want original ZIP", source, err)
	}
	for path, want := range files {
		got, err := repo.GetSiteFile("alpha", "site", path)
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("GetSiteFile(%q) = %q, err=%v; want %q", path, got, err, want)
		}
	}
	if _, err := repo.GetSiteFile("alpha", "site", "../outside"); !errors.Is(err, ErrInvalidSitePath) {
		t.Errorf("traversal read error = %v, want ErrInvalidSitePath", err)
	}

	reopened, err := Open(dataDir)
	if err != nil {
		t.Fatalf("reopen repository: %v", err)
	}
	got, err := reopened.GetSiteFile("alpha", "site", "nested/app.js")
	if err != nil || !bytes.Equal(got, files["nested/app.js"]) {
		t.Errorf("site file after reopen = %q, err=%v", got, err)
	}
}

func TestFailedZIPReplacementKeepsPreviousSite(t *testing.T) {
	dataDir := t.TempDir()
	repo, err := Open(dataDir)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	if _, err := repo.Create(NewArtifact{
		Project:    "alpha",
		Artifact:   "site",
		Format:     FormatZIP,
		Source:     []byte("old archive"),
		EntryPoint: "index.html",
		SiteFiles:  map[string][]byte{"index.html": []byte("old site")},
	}); err != nil {
		t.Fatalf("create ZIP artifact: %v", err)
	}
	repo.failpoint = func(point string) error {
		if point == failBeforeMetadataCommit {
			return errors.New("injected commit failure")
		}
		return nil
	}
	if _, err := repo.Replace(NewArtifact{
		Project:    "alpha",
		Artifact:   "site",
		Format:     FormatZIP,
		Source:     []byte("new archive"),
		EntryPoint: "index.html",
		SiteFiles:  map[string][]byte{"index.html": []byte("new site"), "assets/app.js": []byte("new asset")},
	}); err == nil {
		t.Fatal("replace succeeded, want injected failure")
	}
	repo.failpoint = nil
	_, source, err := repo.Get("alpha", "site")
	if err != nil || !bytes.Equal(source, []byte("old archive")) {
		t.Fatalf("source after failed replace = %q, err=%v", source, err)
	}
	entryPoint, err := repo.GetSiteFile("alpha", "site", "index.html")
	if err != nil || !bytes.Equal(entryPoint, []byte("old site")) {
		t.Fatalf("entry point after failed replace = %q, err=%v", entryPoint, err)
	}
	if _, err := repo.GetSiteFile("alpha", "site", "assets/app.js"); !errors.Is(err, ErrNotFound) {
		t.Errorf("uncommitted file error = %v, want ErrNotFound", err)
	}
	assertPayloadCount(t, filepath.Join(dataDir, "projects", "alpha", "site", "payloads"), 1)
}

func TestReplacePreservesIdentityAndChangesPayload(t *testing.T) {
	dataDir := t.TempDir()
	repo, err := Open(dataDir)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	created, err := repo.Create(NewArtifact{Project: "alpha", Artifact: "site", Format: FormatHTML, Source: []byte("old body")})
	if err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	updated, err := repo.Replace(NewArtifact{Project: "alpha", Artifact: "site", Format: FormatMarkdown, Source: []byte("# new body")})
	if err != nil {
		t.Fatalf("replace artifact: %v", err)
	}
	if updated.Project != created.Project || updated.Artifact != created.Artifact || updated.PublicURL != created.PublicURL {
		t.Errorf("identity changed after replacement: created=%+v updated=%+v", created, updated)
	}
	if !updated.CreatedAt.Equal(created.CreatedAt) {
		t.Errorf("created timestamp changed: %s -> %s", created.CreatedAt, updated.CreatedAt)
	}
	if !updated.ModifiedAt.After(created.ModifiedAt) {
		t.Errorf("modified timestamp did not advance: %s -> %s", created.ModifiedAt, updated.ModifiedAt)
	}
	if updated.Format != FormatMarkdown || updated.Size != int64(len("# new body")) {
		t.Errorf("replacement metadata = %+v, want Markdown and new size", updated)
	}
	gotMetadata, gotSource, err := repo.Get("alpha", "site")
	if err != nil {
		t.Fatalf("get replaced artifact: %v", err)
	}
	if gotMetadata != updated || !bytes.Equal(gotSource, []byte("# new body")) {
		t.Errorf("get after replacement = (%+v, %q), want (%+v, new body)", gotMetadata, gotSource, updated)
	}
	assertPayloadCount(t, filepath.Join(dataDir, "projects", "alpha", "site", "payloads"), 1)
}

func TestFailedReplacementRetainsPreviousArtifactAndCleansNewPayload(t *testing.T) {
	dataDir := t.TempDir()
	repo, err := Open(dataDir)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	if _, err := repo.Create(NewArtifact{Project: "alpha", Artifact: "site", Format: FormatHTML, Source: []byte("committed")}); err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	for _, point := range []string{failAfterPayloadStaged, failAfterPayloadPublished, failBeforeMetadataCommit} {
		t.Run(point, func(t *testing.T) {
			repo.failpoint = func(got string) error {
				if got == point {
					return errors.New("injected failure")
				}
				return nil
			}
			if _, err := repo.Replace(NewArtifact{Project: "alpha", Artifact: "site", Format: FormatHTML, Source: []byte("uncommitted")}); err == nil {
				t.Fatal("replace succeeded, want injected failure")
			}
			repo.failpoint = nil
			_, source, err := repo.Get("alpha", "site")
			if err != nil {
				t.Fatalf("get previous artifact: %v", err)
			}
			if !bytes.Equal(source, []byte("committed")) {
				t.Errorf("source after failed replacement = %q, want committed body", source)
			}
			assertPayloadCount(t, filepath.Join(dataDir, "projects", "alpha", "site", "payloads"), 1)
		})
	}
}

func TestPostCommitCleanupFailureRecoversOnReopen(t *testing.T) {
	dataDir := t.TempDir()
	repo, err := Open(dataDir)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	if _, err := repo.Create(NewArtifact{Project: "alpha", Artifact: "site", Format: FormatHTML, Source: []byte("old")}); err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	repo.failpoint = func(point string) error {
		if point == failBeforeOldPayloadCleanup {
			return errors.New("injected cleanup failure")
		}
		return nil
	}
	if _, err := repo.Replace(NewArtifact{Project: "alpha", Artifact: "site", Format: FormatHTML, Source: []byte("new committed")}); err != nil {
		t.Fatalf("replace with cleanup failure: %v", err)
	}
	repo.failpoint = nil
	_, source, err := repo.Get("alpha", "site")
	if err != nil || !bytes.Equal(source, []byte("new committed")) {
		t.Fatalf("current source = %q, err=%v; want committed replacement", source, err)
	}
	payloadsDir := filepath.Join(dataDir, "projects", "alpha", "site", "payloads")
	assertPayloadCount(t, payloadsDir, 2)

	reopened, err := Open(dataDir)
	if err != nil {
		t.Fatalf("reopen repository: %v", err)
	}
	_, source, err = reopened.Get("alpha", "site")
	if err != nil || !bytes.Equal(source, []byte("new committed")) {
		t.Fatalf("source after recovery = %q, err=%v; want committed replacement", source, err)
	}
	assertPayloadCount(t, payloadsDir, 1)
}

func TestOpenRecoversInterruptedStagingAndDeletion(t *testing.T) {
	dataDir := t.TempDir()
	repo, err := Open(dataDir)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	if _, err := repo.Create(NewArtifact{Project: "alpha", Artifact: "site", Format: FormatHTML, Source: []byte("committed")}); err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	artifactDir := filepath.Join(dataDir, "projects", "alpha", "site")
	payloadsDir := filepath.Join(artifactDir, "payloads")
	if err := os.Mkdir(filepath.Join(payloadsDir, ".stage-crashed"), 0o750); err != nil {
		t.Fatalf("create interrupted staging directory: %v", err)
	}
	if err := os.Mkdir(filepath.Join(payloadsDir, strings.Repeat("0", 32)), 0o750); err != nil {
		t.Fatalf("create orphan payload: %v", err)
	}
	if err := os.WriteFile(filepath.Join(artifactDir, ".metadata-crashed"), []byte("partial"), 0o600); err != nil {
		t.Fatalf("create interrupted metadata file: %v", err)
	}
	uncommittedDir := filepath.Join(dataDir, "projects", "alpha", "pending")
	if err := os.MkdirAll(filepath.Join(uncommittedDir, "payloads", ".stage-crashed"), 0o750); err != nil {
		t.Fatalf("create uncommitted artifact: %v", err)
	}
	tombstone := filepath.Join(dataDir, "projects", "alpha", ".delete-crashed")
	if err := os.MkdirAll(filepath.Join(tombstone, "payloads"), 0o750); err != nil {
		t.Fatalf("create deletion tombstone: %v", err)
	}

	reopened, err := Open(dataDir)
	if err != nil {
		t.Fatalf("recover repository: %v", err)
	}
	_, source, err := reopened.Get("alpha", "site")
	if err != nil || !bytes.Equal(source, []byte("committed")) {
		t.Fatalf("committed source after recovery = %q, err=%v", source, err)
	}
	if _, err := os.Stat(uncommittedDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("uncommitted artifact remains after recovery: stat error=%v", err)
	}
	if _, err := os.Stat(tombstone); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("deletion tombstone remains after recovery: stat error=%v", err)
	}
	if _, err := os.Stat(filepath.Join(artifactDir, ".metadata-crashed")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("metadata temp remains after recovery: stat error=%v", err)
	}
	assertPayloadCount(t, payloadsDir, 1)
	artifacts, err := reopened.ListArtifacts("alpha")
	if err != nil || len(artifacts) != 1 || artifacts[0].Artifact != "site" {
		t.Errorf("artifacts after recovery = %+v, err=%v; want only committed site", artifacts, err)
	}
}

func TestRecoveryDoesNotRemoveOtherPayloadWhenCurrentReferenceIsMissing(t *testing.T) {
	dataDir := t.TempDir()
	repo, err := Open(dataDir)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	if _, err := repo.Create(NewArtifact{Project: "alpha", Artifact: "site", Format: FormatHTML, Source: []byte("last committed")}); err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	record, err := repo.readRecord("alpha", "site")
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	lastPayloadDir := repo.payloadDir("alpha", "site", record.PayloadID)
	missingID := strings.Repeat("f", 32)
	if missingID == record.PayloadID {
		missingID = strings.Repeat("e", 32)
	}
	record.PayloadID = missingID
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("encode altered metadata: %v", err)
	}
	metadataPath := filepath.Join(dataDir, "projects", "alpha", "site", "metadata.json")
	if err := os.WriteFile(metadataPath, encoded, 0o640); err != nil {
		t.Fatalf("write altered metadata: %v", err)
	}
	if _, err := Open(dataDir); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("open with missing current payload error = %v, want ErrCorrupt", err)
	}
	if _, err := os.Stat(filepath.Join(lastPayloadDir, "source")); err != nil {
		t.Errorf("last committed payload was removed during recovery: %v", err)
	}
}

func TestConcurrentReadsAndReplacementsUseCompletePayloads(t *testing.T) {
	repo, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	initial := bytes.Repeat([]byte("old"), 1024)
	first := bytes.Repeat([]byte("first"), 1024)
	second := bytes.Repeat([]byte("second"), 1024)
	if _, err := repo.Create(NewArtifact{Project: "alpha", Artifact: "site", Format: FormatHTML, Source: initial}); err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	allowed := map[string]bool{string(initial): true, string(first): true, string(second): true}
	start := make(chan struct{})
	errorsCh := make(chan error, 128)
	var readers sync.WaitGroup
	for i := 0; i < 8; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			<-start
			for j := 0; j < 40; j++ {
				metadata, source, err := repo.Get("alpha", "site")
				if err != nil {
					errorsCh <- err
					return
				}
				if !allowed[string(source)] || metadata.Size != int64(len(source)) {
					errorsCh <- fmt.Errorf("reader saw incomplete payload: size=%d bytes=%d", metadata.Size, len(source))
					return
				}
			}
		}()
	}
	var writers sync.WaitGroup
	for i := 0; i < 20; i++ {
		writers.Add(1)
		go func(i int) {
			defer writers.Done()
			<-start
			payload := first
			if i%2 == 1 {
				payload = second
			}
			if _, err := repo.Replace(NewArtifact{Project: "alpha", Artifact: "site", Format: FormatHTML, Source: payload}); err != nil {
				errorsCh <- err
			}
		}(i)
	}
	close(start)
	writers.Wait()
	readers.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Errorf("concurrent operation: %v", err)
	}
	metadata, source, err := repo.Get("alpha", "site")
	if err != nil {
		t.Fatalf("get final artifact: %v", err)
	}
	if !allowed[string(source)] || metadata.Size != int64(len(source)) {
		t.Fatalf("final artifact is incomplete: size=%d bytes=%d", metadata.Size, len(source))
	}
}

func TestConcurrentDeleteAndReadsHaveCompleteOutcomes(t *testing.T) {
	repo, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	committed := []byte("complete committed content")
	if _, err := repo.Create(NewArtifact{Project: "alpha", Artifact: "site", Format: FormatHTML, Source: committed}); err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	start := make(chan struct{})
	errorsCh := make(chan error, 16)
	var readers sync.WaitGroup
	for i := 0; i < 8; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			<-start
			for j := 0; j < 40; j++ {
				_, source, err := repo.Get("alpha", "site")
				if errors.Is(err, ErrNotFound) {
					return
				}
				if err != nil {
					errorsCh <- err
					return
				}
				if !bytes.Equal(source, committed) {
					errorsCh <- fmt.Errorf("reader saw unexpected bytes %q", source)
					return
				}
			}
		}()
	}
	close(start)
	if err := repo.DeleteArtifact("alpha", "site"); err != nil {
		t.Fatalf("delete artifact: %v", err)
	}
	readers.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Errorf("concurrent read: %v", err)
	}
	if _, _, err := repo.Get("alpha", "site"); !errors.Is(err, ErrNotFound) {
		t.Errorf("get deleted artifact error = %v, want ErrNotFound", err)
	}
}

func TestDeleteCommitFailureAndTombstoneRecovery(t *testing.T) {
	dataDir := t.TempDir()
	repo, err := Open(dataDir)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	if _, err := repo.Create(NewArtifact{Project: "alpha", Artifact: "site", Format: FormatHTML, Source: []byte("body")}); err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	repo.failpoint = func(point string) error {
		if point == failBeforeDeleteCommit {
			return errors.New("injected delete failure")
		}
		return nil
	}
	if err := repo.DeleteArtifact("alpha", "site"); err == nil {
		t.Fatal("delete succeeded, want injected failure")
	}
	repo.failpoint = nil
	if _, _, err := repo.Get("alpha", "site"); err != nil {
		t.Fatalf("artifact missing after failed delete: %v", err)
	}

	repo.failpoint = func(point string) error {
		if point == failAfterDeleteCommit {
			return errors.New("injected cleanup failure")
		}
		return nil
	}
	if err := repo.DeleteArtifact("alpha", "site"); err != nil {
		t.Fatalf("delete with cleanup failure: %v", err)
	}
	repo.failpoint = nil
	if _, _, err := repo.Get("alpha", "site"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete commit error = %v, want ErrNotFound", err)
	}
	reopened, err := Open(dataDir)
	if err != nil {
		t.Fatalf("recover deletion: %v", err)
	}
	artifacts, err := reopened.ListArtifacts("alpha")
	if err != nil || len(artifacts) != 0 {
		t.Errorf("artifacts after delete recovery = %+v, err=%v", artifacts, err)
	}
}

func assertPayloadCount(t *testing.T, payloadsDir string, want int) {
	t.Helper()
	entries, err := os.ReadDir(payloadsDir)
	if err != nil {
		t.Fatalf("read payload directory %s: %v", payloadsDir, err)
	}
	if len(entries) != want {
		t.Fatalf("payload count in %s = %d, want %d (entries: %v)", payloadsDir, len(entries), want, entries)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
