-- +goose Up
DROP INDEX IF EXISTS runtime_leases_session_idx;
DROP TABLE IF EXISTS runtime_leases;
