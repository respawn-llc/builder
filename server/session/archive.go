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

type ArchivePathError struct {
	Path string
	Err  error
}

func (e *ArchivePathError) Error() string {
	return fmt.Sprintf("Session archive failed at %s: %v", e.Path, e.Err)
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
	if err := validateSessionArchiveDestinationPath(outputPath); err != nil {
		return err
	}
	outputPath = filepath.Clean(outputPath)
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return &ArchivePathError{
			Path: outputPath,
			Err:  err,
		}
	}

	prepared, err := prepareSessionArchive(ctx, sessionID, sessionDir, outputPath)
	if err != nil {
		return err
	}
	return prepared.publish()
}

func PreflightSessionArchiveDestination(outputPath string) error {
	if err := validateSessionArchiveDestinationPath(outputPath); err != nil {
		return err
	}
	outputPath = filepath.Clean(outputPath)
	if _, err := os.Lstat(outputPath); err == nil {
		return &ArchiveOutputExistsError{Path: outputPath}
	} else if !errors.Is(err, os.ErrNotExist) {
		return &ArchivePathError{
			Path: outputPath,
			Err:  err,
		}
	}
	return nil
}

func validateSessionArchiveDestinationPath(outputPath string) error {
	if !filepath.IsAbs(outputPath) {
		return &InvalidArchiveOutputPathError{
			Path:   outputPath,
			Reason: InvalidArchiveOutputPathReasonAbsolute,
		}
	}
	if compressionExtension := filepath.Ext(outputPath); compressionExtension != ".zst" || filepath.Ext(outputPath[:len(outputPath)-len(compressionExtension)]) != ".tar" {
		return &InvalidArchiveOutputPathError{
			Path:   outputPath,
			Reason: InvalidArchiveOutputPathReasonSuffix,
		}
	}
	return nil
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
			Path: outputPath,
			Err:  err,
		}
	}
	prepared := &preparedSessionArchive{outputPath: outputPath, tempPath: temp.Name()}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, prepared.cleanup())
		}
	}()

	tempInfo, err := temp.Stat()
	if err != nil {
		err = errors.Join(err, temp.Close())
		return nil, &ArchivePathError{
			Path: outputPath,
			Err:  err,
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
			Path: outputPath,
			Err:  err,
		}
	}
	tarWriter := tar.NewWriter(encoder)
	resolvedRoot, err := filepath.EvalSymlinks(sessionDir)
	if err != nil {
		err = &ArchivePathError{Path: sessionDir, Err: err}
	} else {
		err = writeSessionArchive(ctx, tarWriter, sessionID, resolvedRoot, tempInfo)
	}
	err = errors.Join(err, tarWriter.Close(), encoder.Close(), temp.Close())
	if err != nil {
		var sourceFailure *ArchivePathError
		if errors.As(err, &sourceFailure) {
			return nil, err
		}
		return nil, &ArchivePathError{
			Path: outputPath,
			Err:  err,
		}
	}
	return prepared, nil
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
			return &ArchivePathError{Path: entryPath, Err: walkErr}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return &ArchivePathError{Path: entryPath, Err: err}
		}
		if info.Mode().IsRegular() && os.SameFile(info, tempInfo) {
			return nil
		}
		linkTarget := ""
		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, err = os.Readlink(entryPath)
			if err != nil {
				return &ArchivePathError{Path: entryPath, Err: err}
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
			return &ArchivePathError{Path: entryPath, Err: err}
		}
		_, copyErr := io.Copy(writer, contextReader{ctx: ctx, reader: file, sourcePath: entryPath})
		if closeErr := file.Close(); closeErr != nil {
			return errors.Join(copyErr, &ArchivePathError{Path: entryPath, Err: closeErr})
		}
		return copyErr
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
				Path: archive.outputPath,
				Err:  err,
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
	if err := os.Remove(archive.tempPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return &ArchivePathError{
			Path: archive.tempPath,
			Err:  err,
		}
	}
	return nil
}

type contextReader struct {
	ctx        context.Context
	reader     io.Reader
	sourcePath string
}

func (r contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	count, err := r.reader.Read(buffer)
	if err != nil && !errors.Is(err, io.EOF) {
		err = &ArchivePathError{Path: r.sourcePath, Err: err}
	}
	return count, err
}
