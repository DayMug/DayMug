package handler

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

func (h *FileHandler) DownloadZip(c *gin.Context) {
	access, ok := h.getUserFileAccess(c)
	if !ok {
		return
	}
	relPaths := c.QueryArray("path")
	if len(relPaths) == 0 {
		relPaths = []string{""}
	}

	sources := make([]compressSource, 0, len(relPaths))
	for _, relPath := range relPaths {
		full, err := access.safePath(relPath)
		if err != nil {
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
			return
		}

		info, err := os.Stat(full)
		if err != nil {
			if os.IsNotExist(err) {
				c.JSON(http.StatusNotFound, gin.H{"error": "path not found"})
			} else {
				respondInternalError(c, "FileHandler.DownloadZip", err)
			}
			return
		}
		sources = append(sources, compressSource{full: full, info: info})
	}

	if len(sources) == 1 && !sources[0].info.IsDir() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path is not a directory"})
		return
	}

	zipName := "Archive.zip"
	if len(sources) == 1 {
		zipName = filepath.Base(sources[0].full) + ".zip"
	}
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, zipName))

	zw := zip.NewWriter(c.Writer)
	defer zw.Close() //nolint:errcheck

	if len(sources) > 1 {
		_ = writeZipSources(zw, sources)
		return
	}

	full := sources[0].full
	_ = filepath.Walk(full, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(full, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		header, err := zip.FileInfoHeader(fi)
		if err != nil {
			return err
		}
		header.Name = rel

		switch {
		case fi.IsDir():
			header.Name += "/"
			_, err = zw.CreateHeader(header)
			return err
		case fi.Mode()&os.ModeSymlink != 0:
			return zipWriteSymlink(zw, header, path)
		default:
			header.Method = zip.Deflate
			w, err := zw.CreateHeader(header)
			if err != nil {
				return err
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close() //nolint:errcheck
			_, err = io.Copy(w, f)
			return err
		}
	})
}

// zipWriteSymlink stores path as a symbolic-link entry: the header already
// carries the symlink mode bits (zip.FileInfoHeader copies them from the
// lstat'd FileInfo) and the entry body is the link *target* string, not the
// target file's contents. Opening the link instead would write the full target
// under a header still flagged as a symlink — which macOS' ditto/Archive
// Utility rejects ("… is too large") and which bloats the archive with a whole
// copy of the target per link.
func zipWriteSymlink(zw *zip.Writer, header *zip.FileHeader, path string) error {
	target, err := os.Readlink(path)
	if err != nil {
		return err
	}
	w, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = w.Write([]byte(filepath.ToSlash(target)))
	return err
}

func (h *FileHandler) Compress(c *gin.Context) {
	access, ok := h.getUserFileAccess(c)
	if !ok {
		return
	}

	var body struct {
		Paths     []string `json:"paths"`
		TargetDir string   `json:"target_dir"`
		Name      string   `json:"name"`
	}
	if !bindJSON(c, &body) {
		return
	}
	if len(body.Paths) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "paths is required"})
		return
	}

	// Resolve every input path up-front so a single bad entry rejects the
	// whole job before we touch the filesystem.
	sources := make([]compressSource, 0, len(body.Paths))
	seen := make(map[string]bool, len(body.Paths))
	for _, p := range body.Paths {
		if p == "" || p == "." {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid source path"})
			return
		}
		full, err := access.safePath(p)
		if err != nil {
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
			return
		}
		if seen[full] {
			continue
		}
		seen[full] = true
		info, err := os.Stat(full)
		if err != nil {
			if os.IsNotExist(err) {
				c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("source not found: %s", p)})
			} else {
				respondInternalError(c, "FileHandler.Compress", err)
			}
			return
		}
		sources = append(sources, compressSource{full: full, info: info})
	}

	targetRel := body.TargetDir
	if targetRel == "" {
		targetRel = "."
	}
	targetDir, err := access.safePath(targetRel)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}
	tInfo, err := os.Stat(targetDir)
	if err != nil || !tInfo.IsDir() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "target_dir is not a directory"})
		return
	}

	// Default the archive name to mirror Finder/Explorer: single selection
	// reuses the source basename; multi-selection falls back to a generic
	// "Archive.zip". An explicit name from the client wins, but it must be
	// a bare filename — no separators, no extension forging.
	name := strings.TrimSpace(body.Name)
	if name == "" {
		if len(sources) == 1 {
			name = filepath.Base(sources[0].full) + ".zip"
		} else {
			name = "Archive.zip"
		}
	}
	if strings.ContainsRune(name, '/') || strings.ContainsRune(name, filepath.Separator) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name must not contain path separators"})
		return
	}
	if !strings.HasSuffix(strings.ToLower(name), ".zip") {
		name += ".zip"
	}
	name = dedupName(targetDir, name)
	dst := filepath.Join(targetDir, name)

	f, err := os.Create(dst)
	if err != nil {
		respondInternalError(c, "FileHandler.Compress", err)
		return
	}
	zw := zip.NewWriter(f)
	if err := writeZipSources(zw, sources); err != nil {
		_ = zw.Close()
		_ = f.Close()
		_ = os.Remove(dst)
		respondInternalError(c, "FileHandler.Compress", err)
		return
	}
	if err := zw.Close(); err != nil {
		_ = f.Close()
		_ = os.Remove(dst)
		respondInternalError(c, "FileHandler.Compress", err)
		return
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(dst)
		respondInternalError(c, "FileHandler.Compress", err)
		return
	}

	rel, err := filepath.Rel(access.workDir, dst)
	if err != nil {
		rel = name
	}
	c.JSON(http.StatusCreated, gin.H{"path": rel, "name": name})
}

// compressSource pairs a resolved absolute path with its stat info; used
// internally by Compress so the resolve and write phases stay separate.
type compressSource struct {
	full string
	info os.FileInfo
}

// writeZipSources writes each source (file or directory tree) into zw,
// prefixing every entry with the source's basename so the archive's top
// level mirrors what the user actually selected.
func writeZipSources(zw *zip.Writer, sources []compressSource) error {
	for _, s := range sources {
		base := filepath.Base(s.full)
		if !s.info.IsDir() {
			if err := zipAddFile(zw, s.full, base, s.info); err != nil {
				return err
			}
			continue
		}
		err := filepath.Walk(s.full, func(p string, fi os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, err := filepath.Rel(s.full, p)
			if err != nil {
				return err
			}
			entryName := base
			if rel != "." {
				entryName = base + "/" + filepath.ToSlash(rel)
			}
			if fi.IsDir() {
				hdr, err := zip.FileInfoHeader(fi)
				if err != nil {
					return err
				}
				hdr.Name = entryName + "/"
				_, err = zw.CreateHeader(hdr)
				return err
			}
			return zipAddFile(zw, p, entryName, fi)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func zipAddFile(zw *zip.Writer, path, name string, fi os.FileInfo) error {
	hdr, err := zip.FileInfoHeader(fi)
	if err != nil {
		return err
	}
	hdr.Name = name
	if fi.Mode()&os.ModeSymlink != 0 {
		return zipWriteSymlink(zw, hdr, path)
	}
	hdr.Method = zip.Deflate
	w, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close() //nolint:errcheck
	_, err = io.Copy(w, in)
	return err
}

// archiveFormat classifies a path by its extension. Only open archive formats
// implemented entirely with the Go standard library are supported — keeping the
// dependency surface zero. Anything else returns "" so the caller can 400.
func archiveFormat(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return "zip"
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return "tar.gz"
	case strings.HasSuffix(lower, ".tar.bz2"), strings.HasSuffix(lower, ".tbz2"):
		return "tar.bz2"
	case strings.HasSuffix(lower, ".tar"):
		return "tar"
	}
	return ""
}

// isMacOSMetadata reports whether an archive entry belongs to the
// `__MACOSX/` resource-fork dump that macOS Finder injects when zipping
// a folder. These entries are never user-visible on macOS and are pure
// junk on every other platform; dropping them also keeps Extract from
// treating a Mac-zipped folder as a multi-top-level archive (which would
// otherwise wrap the real top-level dir in an extra `foo/foo/` layer).
func isMacOSMetadata(name string) bool {
	name = strings.TrimLeft(name, "/")
	return name == "__MACOSX" || name == "__MACOSX/" || strings.HasPrefix(name, "__MACOSX/")
}

// archiveStem strips the archive suffix from a filename so the extracted dir
// can be named after the archive (foo.tar.gz -> foo). Falls back to the bare
// basename when the extension doesn't match a known archive.
func archiveStem(name string) string {
	base := filepath.Base(name)
	lower := strings.ToLower(base)
	for _, ext := range []string{".tar.gz", ".tar.bz2", ".tbz2", ".tgz", ".tar", ".zip"} {
		if strings.HasSuffix(lower, ext) {
			return base[:len(base)-len(ext)]
		}
	}
	return base
}

func (h *FileHandler) Extract(c *gin.Context) {
	access, ok := h.getUserFileAccess(c)
	if !ok {
		return
	}

	var body struct {
		Path string `json:"path"`
	}
	if !bindJSON(c, &body) {
		return
	}
	if body.Path == "" || body.Path == "." {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path is required"})
		return
	}

	format := archiveFormat(body.Path)
	if format == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported archive format"})
		return
	}

	srcFull, err := access.safePath(body.Path)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}
	info, err := os.Stat(srcFull)
	if err != nil {
		if os.IsNotExist(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "archive not found"})
		} else {
			respondInternalError(c, "FileHandler.Extract", err)
		}
		return
	}
	if info.IsDir() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path is a directory"})
		return
	}

	parent := filepath.Dir(srcFull)
	// Unpack into a hidden staging directory inside the parent so the final
	// promotion is a same-filesystem rename. The dot prefix keeps it out of
	// listings if anything crashes mid-extract, and a guaranteed cleanup
	// path means partial trees never leak into the user's workspace.
	staging, err := os.MkdirTemp(parent, ".daymug-extract-*")
	if err != nil {
		respondInternalError(c, "FileHandler.Extract", err)
		return
	}
	cleanupStaging := true
	defer func() {
		if cleanupStaging {
			_ = os.RemoveAll(staging)
		}
	}()

	var extracted int
	switch format {
	case "zip":
		extracted, err = extractZip(srcFull, staging)
	case "tar":
		extracted, err = extractTarFile(srcFull, staging, "")
	case "tar.gz":
		extracted, err = extractTarFile(srcFull, staging, "gzip")
	case "tar.bz2":
		extracted, err = extractTarFile(srcFull, staging, "bzip2")
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("extract: %v", err)})
		return
	}

	destFull, err := promoteExtraction(parent, staging, archiveStem(body.Path))
	if err != nil {
		respondInternalError(c, "FileHandler.Extract", err)
		return
	}
	cleanupStaging = false // promoteExtraction took ownership of staging

	rel, err := filepath.Rel(access.workDir, destFull)
	if err != nil {
		rel = filepath.Base(destFull)
	}
	c.JSON(http.StatusOK, gin.H{"path": rel, "extracted": extracted})
}

// promoteExtraction lifts the contents of staging up into parent and returns
// the resulting absolute path. When the archive shipped its own wrapper —
// the common case after Compress, which prefixes every entry with the source
// basename — staging holds a single top-level entry; promote it directly so
// `foo.zip` round-trips back to `foo/` instead of `foo/foo/` (the russian-
// doll bug). When the archive is loose (multiple top-level entries or
// empty), rename the staging dir itself to a deduped fallback wrapper so the
// parent dir doesn't fill with stray files. The single-entry detection
// depends on the extractors having already filtered macOS `__MACOSX/`
// metadata; otherwise a Finder-zipped folder looks like two top-level
// entries and gets the unwanted extra wrapper.
func promoteExtraction(parent, staging, fallback string) (string, error) {
	entries, err := os.ReadDir(staging)
	if err != nil {
		return "", err
	}

	if len(entries) == 1 {
		name := entries[0].Name()
		finalName := dedupName(parent, name)
		finalPath := filepath.Join(parent, finalName)
		if err := os.Rename(filepath.Join(staging, name), finalPath); err != nil {
			return "", err
		}
		// staging is now empty; remove the dot-prefixed shell so it doesn't
		// linger in the user's directory listing.
		_ = os.Remove(staging)
		return finalPath, nil
	}

	finalName := dedupName(parent, fallback)
	finalPath := filepath.Join(parent, finalName)
	if err := os.Rename(staging, finalPath); err != nil {
		return "", err
	}
	return finalPath, nil
}

// writeArchiveEntry resolves a single entry name against dest with zip-slip
// protection, creates parent dirs, and either materialises a directory or
// copies the entry stream to a regular file with the given mode. Symlinks and
// special files are silently skipped — archives carrying them shouldn't be
// able to plant arbitrary links inside the workspace, and the extracted tree
// stays self-contained.
func writeArchiveEntry(dest, name string, mode os.FileMode, isDir bool, src io.Reader) error {
	target, err := safePath(dest, name)
	if err != nil {
		return fmt.Errorf("entry %q: %w", name, err)
	}
	if isDir {
		return os.MkdirAll(target, 0o755)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	perm := mode.Perm()
	if perm == 0 {
		perm = 0o644
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer out.Close() //nolint:errcheck
	_, err = io.Copy(out, src)
	return err
}

func extractZip(src, dest string) (int, error) {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return 0, err
	}
	defer zr.Close() //nolint:errcheck

	var n int
	for _, f := range zr.File {
		if isMacOSMetadata(f.Name) {
			continue
		}
		isDir := f.FileInfo().IsDir() || strings.HasSuffix(f.Name, "/")
		var rc io.ReadCloser
		if !isDir {
			rc, err = f.Open()
			if err != nil {
				return n, err
			}
		}
		err = writeArchiveEntry(dest, f.Name, f.Mode(), isDir, rc)
		if rc != nil {
			rc.Close() //nolint:errcheck
		}
		if err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func extractTarFile(src, dest, compression string) (int, error) {
	f, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer f.Close() //nolint:errcheck

	var r io.Reader = f
	switch compression {
	case "gzip":
		gz, err := gzip.NewReader(f)
		if err != nil {
			return 0, err
		}
		defer gz.Close() //nolint:errcheck
		r = gz
	case "bzip2":
		r = bzip2.NewReader(f)
	}

	tr := tar.NewReader(r)
	var n int
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return n, err
		}
		if isMacOSMetadata(hdr.Name) {
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := writeArchiveEntry(dest, hdr.Name, os.FileMode(hdr.Mode), true, nil); err != nil {
				return n, err
			}
		case tar.TypeReg:
			if err := writeArchiveEntry(dest, hdr.Name, os.FileMode(hdr.Mode), false, tr); err != nil {
				return n, err
			}
		default:
			// Skip symlinks, hardlinks, devices, fifos — anything that
			// would let the archive reach outside its own tree or plant
			// special files in the workspace.
			continue
		}
		n++
	}
	return n, nil
}
