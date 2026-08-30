package session

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"core/shared/runtimeids"
	"github.com/klauspost/compress/zstd"
)

type InvalidArchiveOutputPathReason uint8

const (
	InvalidArchiveOutputPathReasonAbsolute InvalidArchiveOutputPathReason = iota + 1
	InvalidArchiveOutputPathReasonSuffix
)

type InvalidArchiveOutputPathError struct {
	Path   string
	Reason InvalidArchiveOutputPathReason
}

func (e *InvalidArchiveOutputPathError) Error() string {
	switch e.Reason {
	case InvalidArchiveOutputPathReasonAbsolute:
		return fmt.Sprintf("archive output path %q must be absolute", e.Path)
	case InvalidArchiveOutputPathReasonSuffix:
		return fmt.Sprintf("archive output path %q must end in .tar.zst", e.Path)
	default:
		return fmt.Sprintf("archive output path %q is invalid", e.Path)
	}
}

type ArchiveOutputExistsError struct {
	Path string
}

func (e *ArchiveOutputExistsError) Error() string {
	return fmt.Sprintf("archive output already exists: %s", e.Path)
}

type ArchivePathPhase uint8

const (
	ArchivePathPhaseParent ArchivePathPhase = iota + 1
	ArchivePathPhaseTemp
	ArchivePathPhaseWrite
	ArchivePathPhasePublish
	ArchivePathPhaseCleanup
)

type ArchivePathError struct {
	Path  string
	Phase ArchivePathPhase
	Err   error
}

func (e *ArchivePathError) Error() string {
	return fmt.Sprintf("archive path failure for %s during phase %d: %v", e.Path, e.Phase, e.Err)
}

func (e *ArchivePathError) Unwrap() error {
	return e.Err
}

type preparedSessionArchive struct {
	outputPath string
	tempPath   string
}

func ArchiveSessionDirectory(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	sessionDir string,
	outputPath string,
) error {
	if sessionID.IsZero() {
		return errors.New("Session ID is required")
	}
	if !filepath.IsAbs(sessionDir) {
		return errors.New("Session directory must be absolute")
	}
	if !filepath.IsAbs(outputPath) {
		return &InvalidArchiveOutputPathError{
			Path:   outputPath,
			Reason: InvalidArchiveOutputPathReasonAbsolute,
		}
	}
	if !strings.HasSuffix(outputPath, ".tar.zst") {
		return &InvalidArchiveOutputPathError{
			Path:   outputPath,
			Reason: InvalidArchiveOutputPathReasonSuffix,
		}
	}
	outputPath = filepath.Clean(outputPath)
	if _, err := os.Lstat(outputPath); err == nil {
		return &ArchiveOutputExistsError{Path: outputPath}
	} else if !errors.Is(err, os.ErrNotExist) {
		return &ArchivePathError{
			Path:  outputPath,
			Phase: ArchivePathPhaseParent,
			Err:   err,
		}
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return &ArchivePathError{
			Path:  outputPath,
			Phase: ArchivePathPhaseParent,
			Err:   err,
		}
	}

	prepared, err := prepareSessionArchive(ctx, sessionID, sessionDir, outputPath)
	if err != nil {
		return err
	}
	return prepared.publish()
}

func prepareSessionArchive(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	sessionDir string,
	outputPath string,
) (_ *preparedSessionArchive, resultErr error) {
	temp, err := os.CreateTemp(
		filepath.Dir(outputPath),
		"."+filepath.Base(outputPath)+".tmp-*",
	)
	if err != nil {
		return nil, &ArchivePathError{
			Path:  outputPath,
			Phase: ArchivePathPhaseTemp,
			Err:   err,
		}
	}
	tempPath := temp.Name()
	cleanup := true
	defer func() {
		if !cleanup {
			return
		}
		if cleanupErr := os.Remove(tempPath); cleanupErr != nil && !errors.Is(cleanupErr, os.ErrNotExist) {
			resultErr = errors.Join(resultErr, &ArchivePathError{
				Path:  outputPath,
				Phase: ArchivePathPhaseCleanup,
				Err:   cleanupErr,
			})
		}
	}()

	tempInfo, err := temp.Stat()
	if err != nil {
		err = errors.Join(err, temp.Close())
		return nil, &ArchivePathError{
			Path:  outputPath,
			Phase: ArchivePathPhaseTemp,
			Err:   err,
		}
	}
	encoder, err := zstd.NewWriter(
		temp,
		zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(11)),
		zstd.WithEncoderConcurrency(1),
	)
	if err != nil {
		err = errors.Join(err, temp.Close())
		return nil, &ArchivePathError{
			Path:  outputPath,
			Phase: ArchivePathPhaseWrite,
			Err:   err,
		}
	}
	tarWriter := tar.NewWriter(encoder)
	resolvedRoot, err := filepath.EvalSymlinks(sessionDir)
	if err == nil {
		err = writeSessionArchive(ctx, tarWriter, sessionID, resolvedRoot, tempInfo)
	}
	err = errors.Join(err, tarWriter.Close(), encoder.Close(), temp.Close())
	if err != nil {
		return nil, &ArchivePathError{
			Path:  outputPath,
			Phase: ArchivePathPhaseWrite,
			Err:   err,
		}
	}
	cleanup = false
	return &preparedSessionArchive{
		outputPath: outputPath,
		tempPath:   tempPath,
	}, nil
}

func writeSessionArchive(
	ctx context.Context,
	writer *tar.Writer,
	sessionID runtimeids.SessionID,
	resolvedRoot string,
	tempInfo fs.FileInfo,
) error {
	return filepath.WalkDir(resolvedRoot, func(entryPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() && os.SameFile(info, tempInfo) {
			return nil
		}
		linkTarget := ""
		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, err = os.Readlink(entryPath)
			if err != nil {
				return err
			}
		}
		header, err := tar.FileInfoHeader(info, linkTarget)
		if err != nil {
			return err
		}
		relativePath, err := filepath.Rel(resolvedRoot, entryPath)
		if err != nil {
			return err
		}
		header.Name = sessionID.String()
		if relativePath != "." {
			header.Name = path.Join(header.Name, filepath.ToSlash(relativePath))
		}
		if info.IsDir() {
			header.Name += "/"
		}
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		file, err := os.Open(entryPath)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(writer, contextReader{ctx: ctx, reader: file})
		closeErr := file.Close()
		return errors.Join(copyErr, closeErr)
	})
}

func (archive *preparedSessionArchive) publish() error {
	if err := os.Link(archive.tempPath, archive.outputPath); err != nil {
		cleanupErr := archive.cleanup()
		if errors.Is(err, fs.ErrExist) {
			return errors.Join(
				&ArchiveOutputExistsError{Path: archive.outputPath},
				cleanupErr,
			)
		}
		return errors.Join(
			&ArchivePathError{
				Path:  archive.outputPath,
				Phase: ArchivePathPhasePublish,
				Err:   err,
			},
			cleanupErr,
		)
	}
	if err := archive.cleanup(); err != nil {
		return err
	}
	return nil
}

func (archive *preparedSessionArchive) cleanup() error {
	if archive.tempPath == "" {
		return nil
	}
	tempPath := archive.tempPath
	archive.tempPath = ""
	if err := os.Remove(tempPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return &ArchivePathError{
			Path:  archive.outputPath,
			Phase: ArchivePathPhaseCleanup,
			Err:   err,
		}
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}
