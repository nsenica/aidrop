package zipsite

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"strings"
	"testing"
)

type zipEntrySpec struct {
	name    string
	content []byte
	mode    os.FileMode
}

func TestExtractRootAndWrappedSites(t *testing.T) {
	tests := []struct {
		name      string
		entries   []zipEntrySpec
		wantFiles map[string]string
	}{
		{
			name: "root index and nested static assets",
			entries: []zipEntrySpec{
				{name: "index.html", content: []byte(`<link rel="stylesheet" href="css/site.css"><script src="js/app.js"></script>`)},
				{name: "css/site.css", content: []byte("body { color: red }")},
				{name: "js/app.js", content: []byte("console.log('ok')")},
				{name: "images/logo.png", content: []byte{0, 1, 2, 3}},
				{name: "fonts/site.woff2", content: []byte{4, 5, 6}},
				{name: "nested/page.html", content: []byte("<h1>nested</h1>")},
			},
			wantFiles: map[string]string{
				"index.html":       `<link rel="stylesheet" href="css/site.css"><script src="js/app.js"></script>`,
				"css/site.css":     "body { color: red }",
				"js/app.js":        "console.log('ok')",
				"images/logo.png":  string([]byte{0, 1, 2, 3}),
				"fonts/site.woff2": string([]byte{4, 5, 6}),
				"nested/page.html": "<h1>nested</h1>",
			},
		},
		{
			name: "single wrapper directory",
			entries: []zipEntrySpec{
				{name: "site/", mode: os.ModeDir | 0o755},
				{name: "site/index.html", content: []byte("<h1>wrapped</h1>")},
				{name: "site/assets/nested/app.js", content: []byte("wrapped asset")},
			},
			wantFiles: map[string]string{
				"index.html":           "<h1>wrapped</h1>",
				"assets/nested/app.js": "wrapped asset",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			archive := makeZip(t, tt.entries...)
			site, err := Extract(archive)
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}
			if site.EntryPoint != "index.html" {
				t.Errorf("entry point = %q, want index.html", site.EntryPoint)
			}
			if len(site.Files) != len(tt.wantFiles) {
				t.Fatalf("file count = %d, want %d (%v)", len(site.Files), len(tt.wantFiles), site.Files)
			}
			for path, want := range tt.wantFiles {
				if got := string(site.Files[path]); got != want {
					t.Errorf("file %q = %q, want %q", path, got, want)
				}
			}
		})
	}
}

func TestExtractRejectsUnsafeArchiveEntries(t *testing.T) {
	tests := []struct {
		name    string
		entries []zipEntrySpec
	}{
		{name: "parent traversal", entries: []zipEntrySpec{{name: "index.html", content: []byte("ok")}, {name: "../outside", content: []byte("bad")}}},
		{name: "nested traversal", entries: []zipEntrySpec{{name: "index.html", content: []byte("ok")}, {name: "assets/../../outside", content: []byte("bad")}}},
		{name: "absolute unix path", entries: []zipEntrySpec{{name: "index.html", content: []byte("ok")}, {name: "/tmp/outside", content: []byte("bad")}}},
		{name: "windows drive path", entries: []zipEntrySpec{{name: "index.html", content: []byte("ok")}, {name: "C:/outside", content: []byte("bad")}}},
		{name: "windows backslash path", entries: []zipEntrySpec{{name: "index.html", content: []byte("ok")}, {name: `C:\outside`, content: []byte("bad")}}},
		{name: "unc path", entries: []zipEntrySpec{{name: "index.html", content: []byte("ok")}, {name: `\\server\share`, content: []byte("bad")}}},
		{name: "repeated separator", entries: []zipEntrySpec{{name: "index.html", content: []byte("ok")}, {name: "assets//image.png", content: []byte("bad")}}},
		{name: "dot component", entries: []zipEntrySpec{{name: "index.html", content: []byte("ok")}, {name: "assets/./image.png", content: []byte("bad")}}},
		{name: "symlink", entries: []zipEntrySpec{{name: "index.html", content: []byte("ok")}, {name: "link", content: []byte("target"), mode: os.ModeSymlink | 0o777}}},
		{name: "fifo", entries: []zipEntrySpec{{name: "index.html", content: []byte("ok")}, {name: "pipe", content: []byte("bad"), mode: os.ModeNamedPipe | 0o600}}},
		{name: "exact duplicate", entries: []zipEntrySpec{{name: "index.html", content: []byte("one")}, {name: "index.html", content: []byte("two")}}},
		{name: "case folded collision", entries: []zipEntrySpec{{name: "index.html", content: []byte("ok")}, {name: "assets/logo.png", content: []byte("one")}, {name: "assets/LOGO.png", content: []byte("two")}}},
		{name: "implicit directory case collision", entries: []zipEntrySpec{{name: "index.html", content: []byte("ok")}, {name: "Assets/a.css", content: []byte("one")}, {name: "assets/b.css", content: []byte("two")}}},
		{name: "file directory conflict", entries: []zipEntrySpec{{name: "index.html", content: []byte("ok")}, {name: "assets", content: []byte("file")}, {name: "assets/site.css", content: []byte("child")}}},
		{name: "missing entry point", entries: []zipEntrySpec{{name: "readme.txt", content: []byte("no site")}}},
		{name: "multiple wrappers", entries: []zipEntrySpec{{name: "one/index.html", content: []byte("one")}, {name: "two/site.css", content: []byte("two")}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			archive := makeZip(t, tt.entries...)
			if _, err := Extract(archive); !errors.Is(err, ErrInvalidArchive) {
				t.Fatalf("Extract error = %v, want ErrInvalidArchive", err)
			}
		})
	}
}

func TestExtractRejectsObviousSensitiveFiles(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "root environment file", path: ".env"},
		{name: "production environment variant", path: "config/.env.production.local"},
		{name: "nested git metadata", path: "site/.git/config"},
		{name: "ssh RSA identity", path: "keys/id_rsa"},
		{name: "ssh Ed25519 identity", path: "keys/id_ed25519"},
		{name: "private key extension", path: "certs/server.key"},
		{name: "private PEM name", path: "certs/server-private.pem"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			archive := makeZip(t,
				zipEntrySpec{name: "index.html", content: []byte("site")},
				zipEntrySpec{name: tt.path, content: []byte("sensitive")},
			)
			if _, err := Extract(archive); !errors.Is(err, ErrInvalidArchive) {
				t.Fatalf("Extract error = %v, want ErrInvalidArchive", err)
			}
		})
	}
}

func TestExtractAllowsEnvironmentTemplatesAndPublicKeyFiles(t *testing.T) {
	archive := makeZip(t,
		zipEntrySpec{name: "index.html", content: []byte("site")},
		zipEntrySpec{name: ".env.example", content: []byte("API_URL=")},
		zipEntrySpec{name: ".env.sample", content: []byte("PORT=")},
		zipEntrySpec{name: ".gitignore", content: []byte("node_modules/")},
		zipEntrySpec{name: "certs/certificate.pem", content: []byte("public certificate")},
		zipEntrySpec{name: "keys/id_ed25519.pub", content: []byte("public ssh key")},
		zipEntrySpec{name: "assets/app.js", content: []byte("console.log('safe')")},
	)
	if _, err := Extract(archive); err != nil {
		t.Fatalf("Extract rejected ordinary website files: %v", err)
	}
}

func TestExtractEnforcesResourceLimits(t *testing.T) {
	t.Run("compressed size", func(t *testing.T) {
		if _, err := Extract(make([]byte, MaxCompressedSize+1)); !errors.Is(err, ErrLimitExceeded) {
			t.Fatalf("Extract error = %v, want ErrLimitExceeded", err)
		}
	})
	t.Run("entry count", func(t *testing.T) {
		entries := make([]zipEntrySpec, 0, MaxEntries+1)
		for i := 0; i < MaxEntries+1; i++ {
			entries = append(entries, zipEntrySpec{name: fmt.Sprintf("files/%04d.txt", i)})
		}
		if _, err := Extract(makeZip(t, entries...)); !errors.Is(err, ErrLimitExceeded) {
			t.Fatalf("Extract error = %v, want ErrLimitExceeded", err)
		}
	})
	t.Run("path depth", func(t *testing.T) {
		path := strings.TrimSuffix(strings.Repeat("d/", 16), "/") + "/index.html"
		if _, err := Extract(makeZip(t, zipEntrySpec{name: path, content: []byte("deep")}, zipEntrySpec{name: "index.html", content: []byte("root")})); !errors.Is(err, ErrInvalidArchive) {
			t.Fatalf("Extract error = %v, want ErrInvalidArchive", err)
		}
	})
	t.Run("per file expansion ratio", func(t *testing.T) {
		archive := makeZip(t, zipEntrySpec{name: "index.html", content: bytes.Repeat([]byte("x"), 1<<20)})
		if _, err := Extract(archive); !errors.Is(err, ErrLimitExceeded) {
			t.Fatalf("Extract error = %v, want ErrLimitExceeded", err)
		}
	})
	t.Run("declared extracted size", func(t *testing.T) {
		archive := makeRawZip(t, "index.html", []byte("tiny"), MaxExtractedSize+1)
		if _, err := Extract(archive); !errors.Is(err, ErrLimitExceeded) {
			t.Fatalf("Extract error = %v, want ErrLimitExceeded", err)
		}
	})
	t.Run("understated extracted size cannot bypass streaming validation", func(t *testing.T) {
		archive := makeRawZip(t, "index.html", bytes.Repeat([]byte("x"), 1<<20), 1)
		if _, err := Extract(archive); err == nil {
			t.Fatal("Extract accepted a payload larger than its declared header size")
		}
	})
}

func makeZip(t *testing.T, entries ...zipEntrySpec) []byte {
	t.Helper()
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for _, entry := range entries {
		method := uint16(zip.Deflate)
		if strings.HasSuffix(entry.name, "/") {
			method = zip.Store
		}
		header := &zip.FileHeader{Name: entry.name, Method: method}
		if entry.mode != 0 {
			header.SetMode(entry.mode)
		}
		file, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatalf("create ZIP entry %q: %v", entry.name, err)
		}
		if _, err := file.Write(entry.content); err != nil {
			t.Fatalf("write ZIP entry %q: %v", entry.name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close ZIP archive: %v", err)
	}
	return archive.Bytes()
}

func makeRawZip(t *testing.T, name string, contents []byte, declaredSize uint64) []byte {
	t.Helper()
	var compressed bytes.Buffer
	flateWriter, err := flate.NewWriter(&compressed, flate.BestSpeed)
	if err != nil {
		t.Fatalf("create flate writer: %v", err)
	}
	if _, err := flateWriter.Write(contents); err != nil {
		t.Fatalf("compress ZIP entry: %v", err)
	}
	if err := flateWriter.Close(); err != nil {
		t.Fatalf("close flate writer: %v", err)
	}
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	header := &zip.FileHeader{
		Name:               name,
		Method:             zip.Deflate,
		CRC32:              crc32.ChecksumIEEE(contents),
		CompressedSize:     uint32(compressed.Len()),
		CompressedSize64:   uint64(compressed.Len()),
		UncompressedSize:   uint32(declaredSize),
		UncompressedSize64: declaredSize,
	}
	entry, err := writer.CreateRaw(header)
	if err != nil {
		t.Fatalf("create raw ZIP entry: %v", err)
	}
	if _, err := io.Copy(entry, &compressed); err != nil {
		t.Fatalf("write raw ZIP content: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close raw ZIP archive: %v", err)
	}
	return archive.Bytes()
}
