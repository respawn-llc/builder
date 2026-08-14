-- +goose Up

ALTER TABLE sessions
DROP COLUMN last_sequence;
