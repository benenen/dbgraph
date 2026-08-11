package status

type Snapshot struct {
	SchemaVersion      int
	SQLiteVersion      string
	JournalMode        string
	ForeignKeysEnabled bool
}
