package store

import (
	"database/sql"
	"time"

	_ "modernc.org/sqlite"
)

// Store — локальный журнал переписок (для рекапа) и служебных событий.
type Store struct {
	db *sql.DB
}

type Message struct {
	ChatKey   string
	ChatTitle string
	Sender    string
	Out       bool
	ByBot     bool
	Text      string
	At        time.Time
}

type Event struct {
	ChatKey   string
	ChatTitle string
	Kind      string // defer, limit, error
	Note      string
	At        time.Time
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`
CREATE TABLE IF NOT EXISTS messages (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	chat_key TEXT NOT NULL,
	chat_title TEXT NOT NULL,
	sender TEXT NOT NULL,
	is_out INTEGER NOT NULL,
	by_bot INTEGER NOT NULL,
	text TEXT NOT NULL,
	ts INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS messages_ts ON messages(ts);
CREATE TABLE IF NOT EXISTS events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	chat_key TEXT NOT NULL,
	chat_title TEXT NOT NULL,
	kind TEXT NOT NULL,
	note TEXT NOT NULL,
	ts INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS kv (k TEXT PRIMARY KEY, v TEXT NOT NULL);
`)
	if err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Store) AddMessage(m Message) error {
	_, err := s.db.Exec(`INSERT INTO messages(chat_key, chat_title, sender, is_out, by_bot, text, ts) VALUES(?,?,?,?,?,?,?)`,
		m.ChatKey, m.ChatTitle, m.Sender, b2i(m.Out), b2i(m.ByBot), m.Text, m.At.Unix())
	return err
}

func (s *Store) AddEvent(e Event) error {
	_, err := s.db.Exec(`INSERT INTO events(chat_key, chat_title, kind, note, ts) VALUES(?,?,?,?,?)`,
		e.ChatKey, e.ChatTitle, e.Kind, e.Note, e.At.Unix())
	return err
}

func (s *Store) MessagesBetween(from, to time.Time) ([]Message, error) {
	rows, err := s.db.Query(`SELECT chat_key, chat_title, sender, is_out, by_bot, text, ts FROM messages WHERE ts > ? AND ts <= ? ORDER BY ts, id`,
		from.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var isOut, byBot int
		var ts int64
		if err := rows.Scan(&m.ChatKey, &m.ChatTitle, &m.Sender, &isOut, &byBot, &m.Text, &ts); err != nil {
			return nil, err
		}
		m.Out, m.ByBot, m.At = isOut == 1, byBot == 1, time.Unix(ts, 0)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) EventsBetween(from, to time.Time) ([]Event, error) {
	rows, err := s.db.Query(`SELECT chat_key, chat_title, kind, note, ts FROM events WHERE ts > ? AND ts <= ? ORDER BY ts, id`,
		from.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var ts int64
		if err := rows.Scan(&e.ChatKey, &e.ChatTitle, &e.Kind, &e.Note, &ts); err != nil {
			return nil, err
		}
		e.At = time.Unix(ts, 0)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) Get(k string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT v FROM kv WHERE k = ?`, k).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func (s *Store) Set(k, v string) error {
	_, err := s.db.Exec(`INSERT INTO kv(k, v) VALUES(?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, k, v)
	return err
}
