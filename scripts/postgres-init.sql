-- Runs once, when the Postgres volume is first created.
-- A separate database for integration tests, so tests never touch dev data.
CREATE DATABASE clouddeploy_test OWNER clouddeploy;
