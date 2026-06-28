BEGIN TRANSACTION;
CREATE TABLE events(id INTEGER PRIMARY KEY, name TEXT);
INSERT INTO events VALUES(1,'created');
COMMIT;
PRAGMA foreign_keys=ON;
CREATE TRIGGER events_ai AFTER INSERT ON events
BEGIN
  INSERT INTO events VALUES(new.id + 1000, 'trigger; body');
  UPDATE events SET name = CASE WHEN name = 'created' THEN 'created; again' ELSE name END WHERE id = new.id;
END;
