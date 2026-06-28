PRAGMA defer_foreign_keys=TRUE;
PRAGMA foreign_keys=OFF;
BEGIN TRANSACTION;
CREATE TABLE users (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL
);
INSERT INTO users VALUES
  (1,'Ada'),
  (2,'Grace; Hopper');
DELETE FROM sqlite_sequence;
COMMIT;
