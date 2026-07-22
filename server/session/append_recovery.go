package session

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	appendRecoveryFile          = "append-recovery.json"
	appendRecoveryRecordVersion = 1
	maxAppendRecoveryRecordSize = int64(16 << 20)
	maxAppendRecoverySuffixSize = int64(16 << 20)
	appendRecoveryPrepared      = appendRecoveryPhase("prepared")
	appendRecoveryCommitted     = appendRecoveryPhase("committed")
)

type appendRecoveryPhase string

type appendRecoveryEvents struct {
	StartOffset   int64  `json:"start_offset"`
	EndOffset     int64  `json:"end_offset"`
	EventCount    int    `json:"event_count"`
	FirstSequence int64  `json:"first_sequence"`
	LastSequence  int64  `json:"last_sequence"`
	SHA256        string `json:"sha256"`
}

type appendRecoveryMeta struct {
	Meta   Meta   `json:"meta"`
	SHA256 string `json:"sha256"`
}

type appendRecoveryRecord struct {
	Version int                   `json:"version"`
	Phase   appendRecoveryPhase   `json:"phase"`
	Pre     appendRecoveryMeta    `json:"pre"`
	Post    appendRecoveryMeta    `json:"post"`
	Events  *appendRecoveryEvents `json:"events,omitempty"`
}

func (s *Store) newAppendRecoveryRecord(preMeta, postMeta Meta, phase appendRecoveryPhase, events *appendRecoveryEvents) (appendRecoveryRecord, error) {
	pre, preErr := appendRecoveryMetaOf(preMeta)
	post, postErr := appendRecoveryMetaOf(postMeta)
	record := appendRecoveryRecord{
		Version: appendRecoveryRecordVersion,
		Phase:   phase,
		Pre:     pre,
		Post:    post,
		Events:  events,
	}
	return record, errors.Join(preErr, postErr, record.validate(preMeta.SessionID))
}

func appendRecoveryMetaOf(meta Meta) (appendRecoveryMeta, error) {
	meta.CreatedAt = meta.CreatedAt.UTC().Truncate(time.Millisecond)
	meta.UpdatedAt = meta.UpdatedAt.UTC().Truncate(time.Millisecond)
	encoded, err := json.Marshal(meta)
	return appendRecoveryMeta{Meta: meta, SHA256: digestBytes(encoded)}, err
}

func digestMeta(meta Meta) (string, error) {
	snapshot, err := appendRecoveryMetaOf(meta)
	return snapshot.SHA256, err
}

func digestBytes(data []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func validateRecoveryTarget(path string) error {
	fp, err := openRegularSessionFile(path, "append recovery record")
	if err == nil {
		return fp.Close()
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func syncRecoveryDirectory(path string) error {
	return syncSessionDirectory(filepath.Dir(path))
}

func (s *Store) writeAppendRecoveryRecord(record appendRecoveryRecord) error {
	path := filepath.Join(s.sessionDir, appendRecoveryFile)
	if err := validateRecoveryTarget(path); err != nil {
		return err
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if int64(len(encoded)) > maxAppendRecoveryRecordSize {
		return errors.New("append recovery record exceeds size limit")
	}
	tmp, err := os.CreateTemp(s.sessionDir, "."+appendRecoveryFile+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	_, err = tmp.Write(encoded)
	if err := syncAndClose(tmp, err); err != nil {
		return errors.Join(err, os.Remove(tmpPath))
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return errors.Join(err, os.Remove(tmpPath))
	}
	return syncRecoveryDirectory(path)
}

func syncAndClose(fp *os.File, err error) error {
	if fp == nil {
		return errors.Join(err, os.ErrInvalid)
	}
	return errors.Join(err, fp.Sync(), fp.Close())
}

func (s *Store) clearAppendRecoveryRecord() error {
	path := filepath.Join(s.sessionDir, appendRecoveryFile)
	if err := validateRecoveryTarget(path); err != nil {
		return err
	}
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return syncRecoveryDirectory(path)
}

func (s *Store) readAppendRecoveryRecord() (*appendRecoveryRecord, error) {
	fp, err := openRegularSessionFile(filepath.Join(s.sessionDir, appendRecoveryFile), "append recovery record")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	encoded, readErr := io.ReadAll(io.LimitReader(fp, maxAppendRecoveryRecordSize+1))
	if err := errors.Join(readErr, fp.Close()); err != nil {
		return nil, err
	}
	if int64(len(encoded)) > maxAppendRecoveryRecordSize {
		return nil, errors.New("append recovery record exceeds size limit")
	}
	record := new(appendRecoveryRecord)
	if err := json.Unmarshal(encoded, record); err != nil {
		return nil, err
	}
	if err := record.validate(s.meta.SessionID); err != nil {
		return nil, err
	}
	return record, nil
}

func (record appendRecoveryRecord) validate(sessionID string) error {
	preDigest, preErr := digestMeta(record.Pre.Meta)
	postDigest, postErr := digestMeta(record.Post.Meta)
	events := record.Events
	sessionID = strings.TrimSpace(sessionID)
	switch {
	case record.Version != appendRecoveryRecordVersion:
		return fmt.Errorf("append recovery version %d is unsupported", record.Version)
	case record.Pre.Meta.SessionID != sessionID || record.Post.Meta.SessionID != sessionID:
		return fmt.Errorf("append recovery metadata does not match session %q", sessionID)
	case record.Phase != appendRecoveryPrepared && record.Phase != appendRecoveryCommitted:
		return fmt.Errorf("append recovery phase %q is invalid", record.Phase)
	case preErr != nil || postErr != nil || preDigest != record.Pre.SHA256 || postDigest != record.Post.SHA256:
		return errors.Join(preErr, postErr, errors.New("append recovery metadata digest is invalid"))
	case events == nil && record.Phase != appendRecoveryCommitted:
		return errors.New("metadata-only recovery record must be committed")
	case events != nil && (events.StartOffset < 0 || events.EndOffset <= events.StartOffset ||
		events.EndOffset-events.StartOffset > maxAppendRecoverySuffixSize ||
		events.EventCount <= 0 || events.FirstSequence <= 0 ||
		events.LastSequence < events.FirstSequence || strings.TrimSpace(events.SHA256) == ""):
		return errors.New("append recovery event identity is invalid")
	default:
		return nil
	}
}

func inspectAppendRecoverySuffix(path string, events appendRecoveryEvents, phase appendRecoveryPhase) (exact bool, err error) {
	fp, err := openRegularSessionFile(path, "events file")
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, fp.Close()) }()
	info, err := fp.Stat()
	if err != nil {
		return false, err
	}
	size := info.Size()
	if size < events.StartOffset || size > events.EndOffset {
		return false, fmt.Errorf("events file size %d is outside recovery range [%d,%d]", size, events.StartOffset, events.EndOffset)
	}
	if size != events.EndOffset || phase == appendRecoveryPrepared {
		return size == events.EndOffset, nil
	}
	hash := sha256.New()
	decoder := json.NewDecoder(io.TeeReader(
		io.NewSectionReader(fp, events.StartOffset, events.EndOffset-events.StartOffset),
		hash,
	))
	count := 0
	firstSequence := int64(0)
	lastSequence := int64(0)
	for {
		var encoded json.RawMessage
		err := decoder.Decode(&encoded)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return false, fmt.Errorf("parse committed event suffix: %w", err)
		}
		event, err := decodeEventRecordV1(encoded)
		if err != nil {
			return false, fmt.Errorf("parse committed typed event suffix: %w", err)
		}
		count++
		if count == 1 {
			firstSequence = event.Seq()
		}
		lastSequence = event.Seq()
	}
	if count != events.EventCount || firstSequence != events.FirstSequence || lastSequence != events.LastSequence ||
		fmt.Sprintf("%x", hash.Sum(nil)) != events.SHA256 {
		return false, errors.New("committed event suffix identity is invalid")
	}
	return true, nil
}

func truncateAppendRecoverySuffix(path string, offset int64) error {
	if err := validateRecoveryTarget(path); err != nil {
		return err
	}
	fp, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	return syncAndClose(fp, fp.Truncate(offset))
}

func (s *Store) recoverAppendTransaction() error {
	record, err := s.readAppendRecoveryRecord()
	if err != nil {
		return s.recoveryError("read recovery record", err)
	}
	if record == nil {
		return nil
	}
	currentDigest, err := digestMeta(s.meta)
	if err != nil || currentDigest != record.Pre.SHA256 && currentDigest != record.Post.SHA256 {
		return s.recoveryError("validate metadata state", errors.Join(err, errors.New("persisted metadata matches neither recovery snapshot")))
	}
	selected := record.Post.Meta
	if record.Events != nil {
		exact, err := inspectAppendRecoverySuffix(s.eventsFP, *record.Events, record.Phase)
		if err != nil {
			return s.recoveryError("inspect event suffix", err)
		}
		if record.Phase == appendRecoveryPrepared || !exact {
			if err := truncateAppendRecoverySuffix(s.eventsFP, record.Events.StartOffset); err != nil {
				return s.recoveryError("truncate event suffix", err)
			}
			selected = record.Pre.Meta
		}
	}
	s.meta = cloneMeta(selected)
	return s.observePersistence(&persistenceObservation{snapshot: s.persistenceSnapshotLocked()})
}
