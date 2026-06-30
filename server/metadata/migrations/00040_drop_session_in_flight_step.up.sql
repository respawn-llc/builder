-- +goose Up

ALTER TABLE sessions DROP COLUMN in_flight_step;
