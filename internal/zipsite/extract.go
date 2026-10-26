package zipsite

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"

	"aidrop/internal/storage"
)

const (
	MaxCompressedSize = 25 << 20
	MaxExtractedSize  = 100 << 20
	MaxEntries        = 1000
	MaxExpansionRatio = 100
)

var (
	ErrInvalidArchive = errors.New("invalid ZIP website")
	ErrLimitExceeded  = errors.New("ZIP website exceeds safety limits")
)

// Site is a validated, extracted static site. Source bytes remain the original ZIP.
type Site struct {
	Files      map[string][]byte
	EntryPoint string
}

type validatedEntry struct {
	file   *zip.File
	path   string
	isDir  bool
	method uint16
}

type pathKind uint8

const (
	pathUnknown pathKind = iota
	pathDirectory
	pathFile
)

type pathNode struct {
	actual   string
	kind     pathKind
	explicit bool
}

// Extract validates and expands a ZIP archive using the documented resource limits.
func Extract(source []byte) (Site, error) {
	if len(source) == 0 {
		return Site{}, fmt.Errorf("%w: empty archive", ErrInvalidArchive)
	}
	if len(source) > MaxCompressedSize {
		return Site{}, ErrLimitExceeded
	}
	reader, err := zip.NewReader(bytes.NewReader(source), int64(len(source)))
	if err != nil {
		return Site{}, fmt.Errorf("%w: parse central directory", ErrInvalidArchive)
	}
	if len(reader.File) == 0 {
		return Site{}, fmt.Errorf("%w: archive is empty", ErrInvalidArchive)
	}
	if len(reader.File) > MaxEntries {
		return Site{}, ErrLimitExceeded
	}

	entries := make([]validatedEntry, 0, len(reader.File))
	pathNodes := make(map[string]*pathNode, len(reader.File)*2)
	var compressedTotal uint64
	var declaredExtracted uint64
	for _, file := range reader.File {
		entry, err := validateEntry(file, pathNodes)
		if err != nil {
			return Site{}, err
		}
		if entry.isDir {
			if file.CompressedSize64 != 0 || file.UncompressedSize64 != 0 {
				return Site{}, fmt.Errorf("%w: directory entries must not contain data", ErrInvalidArchive)
			}
			entries = append(entries, entry)
			continue
		}
		if file.CompressedSize64 > uint64(len(source))-compressedTotal {
			return Site{}, fmt.Errorf("%w: compressed sizes exceed archive length", ErrInvalidArchive)
		}
		compressedTotal += file.CompressedSize64
		if file.UncompressedSize64 > MaxExtractedSize-declaredExtracted {
			return Site{}, ErrLimitExceeded
		}
		declaredExtracted += file.UncompressedSize64
		if ratioExceeded(file.UncompressedSize64, file.CompressedSize64) {
			return Site{}, ErrLimitExceeded
		}
		entries = append(entries, entry)
	}

	entryPoint, prefix, err := findEntryPoint(entries)
	if err != nil {
		return Site{}, err
	}

	files := make(map[string][]byte)
	var extractedTotal uint64
	for _, entry := range entries {
		if entry.isDir {
			continue
		}
		body, size, err := readEntry(entry.file, extractedTotal, compressedTotal)
		if err != nil {
			return Site{}, err
		}
		extractedTotal += size
		sitePath := entry.path
		if prefix != "" {
			sitePath = strings.TrimPrefix(sitePath, prefix+"/")
		}
		if err := storage.ValidateSitePath(sitePath); err != nil {
			return Site{}, fmt.Errorf("%w: invalid extracted path", ErrInvalidArchive)
		}
		files[sitePath] = body
	}
	if _, ok := files[entryPoint]; !ok {
		return Site{}, fmt.Errorf("%w: entry point is missing", ErrInvalidArchive)
	}
	return Site{Files: files, EntryPoint: entryPoint}, nil
}

func validateEntry(file *zip.File, nodes map[string]*pathNode) (validatedEntry, error) {
	rawPath := file.Name
	mode := file.Mode()
	if rawPath == "" || strings.HasPrefix(rawPath, "/") || strings.ContainsAny(rawPath, "\\:") || strings.IndexByte(rawPath, 0) >= 0 {
		return validatedEntry{}, fmt.Errorf("%w: unsafe archive path", ErrInvalidArchive)
	}
	if mode&os.ModeSymlink != 0 || (mode.Type() != 0 && !mode.IsDir() && !mode.IsRegular()) {
		return validatedEntry{}, fmt.Errorf("%w: non-regular archive entry", ErrInvalidArchive)
	}
	isDir := file.FileInfo().IsDir()
	path := rawPath
	if isDir && strings.HasSuffix(path, "/") {
		path = strings.TrimSuffix(path, "/")
	}
	if err := storage.ValidateSitePath(path); err != nil {
		return validatedEntry{}, fmt.Errorf("%w: unsafe archive path", ErrInvalidArchive)
	}
	if err := registerPath(path, isDir, nodes); err != nil {
		return validatedEntry{}, err
	}
	return validatedEntry{file: file, path: path, isDir: isDir, method: file.Method}, nil
}

func registerPath(sitePath string, isDir bool, nodes map[string]*pathNode) error {
	parts := strings.Split(sitePath, "/")
	for i := range parts {
		actual := strings.Join(parts[:i+1], "/")
		key := foldPath(actual)
		node := nodes[key]
		if node == nil {
			node = &pathNode{actual: actual}
			nodes[key] = node
		} else if node.actual != actual {
			return fmt.Errorf("%w: case-folding path collision", ErrInvalidArchive)
		}
		isLast := i == len(parts)-1
		if !isLast {
			if node.kind == pathFile {
				return fmt.Errorf("%w: file/directory path conflict", ErrInvalidArchive)
			}
			if node.kind == pathUnknown {
				node.kind = pathDirectory
			}
			continue
		}
		if node.explicit {
			return fmt.Errorf("%w: duplicate archive path", ErrInvalidArchive)
		}
		if isDir {
			if node.kind == pathFile {
				return fmt.Errorf("%w: file/directory path conflict", ErrInvalidArchive)
			}
			node.kind = pathDirectory
		} else {
			if node.kind == pathDirectory {
				return fmt.Errorf("%w: file/directory path conflict", ErrInvalidArchive)
			}
			for existing := range nodes {
				if strings.HasPrefix(existing, key+"/") {
					return fmt.Errorf("%w: file/directory path conflict", ErrInvalidArchive)
				}
			}
			node.kind = pathFile
		}
		node.explicit = true
	}
	return nil
}

func foldPath(value string) string {
	return strings.Map(func(r rune) rune {
		least := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			if next < least {
				least = next
			}
		}
		return least
	}, value)
}

func findEntryPoint(entries []validatedEntry) (entryPoint, wrapperPrefix string, err error) {
	rootIndex := false
	topLevels := make(map[string]struct{})
	files := make(map[string]bool)
	for _, entry := range entries {
		top := strings.Split(entry.path, "/")[0]
		topLevels[top] = struct{}{}
		if !entry.isDir {
			files[entry.path] = true
			if entry.path == "index.html" {
				rootIndex = true
			}
		}
	}
	if rootIndex {
		return "index.html", "", nil
	}
	if len(topLevels) != 1 {
		return "", "", fmt.Errorf("%w: archive must have a root or single-wrapper index.html", ErrInvalidArchive)
	}
	for wrapper := range topLevels {
		indexPath := wrapper + "/index.html"
		if !files[indexPath] {
			return "", "", fmt.Errorf("%w: archive is missing index.html", ErrInvalidArchive)
		}
		return "index.html", wrapper, nil
	}
	return "", "", fmt.Errorf("%w: archive is missing index.html", ErrInvalidArchive)
}

func readEntry(file *zip.File, totalSoFar, compressedTotal uint64) ([]byte, uint64, error) {
	rc, err := file.Open()
	if err != nil {
		return nil, 0, fmt.Errorf("%w: open archive entry", ErrInvalidArchive)
	}
	defer rc.Close()

	var contents bytes.Buffer
	buffer := make([]byte, 32<<10)
	var fileExpanded uint64
	for {
		n, readErr := rc.Read(buffer)
		if n > 0 {
			if totalSoFar > MaxExtractedSize || fileExpanded > MaxExtractedSize-totalSoFar || uint64(n) > MaxExtractedSize-totalSoFar-fileExpanded {
				return nil, 0, ErrLimitExceeded
			}
			fileExpanded += uint64(n)
			if ratioExceeded(fileExpanded, file.CompressedSize64) || ratioExceeded(totalSoFar+fileExpanded, compressedTotal) {
				return nil, 0, ErrLimitExceeded
			}
			if _, err := contents.Write(buffer[:n]); err != nil {
				return nil, 0, err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, 0, fmt.Errorf("%w: decompress archive entry", ErrInvalidArchive)
		}
	}
	if fileExpanded != file.UncompressedSize64 {
		return nil, 0, fmt.Errorf("%w: archive entry size does not match its header", ErrInvalidArchive)
	}
	if err := rc.Close(); err != nil {
		return nil, 0, fmt.Errorf("%w: close archive entry", ErrInvalidArchive)
	}
	return contents.Bytes(), fileExpanded, nil
}

func ratioExceeded(expanded, compressed uint64) bool {
	if expanded == 0 {
		return false
	}
	if compressed == 0 {
		return true
	}
	quotient := expanded / compressed
	return quotient > MaxExpansionRatio || (quotient == MaxExpansionRatio && expanded%compressed != 0)
}
