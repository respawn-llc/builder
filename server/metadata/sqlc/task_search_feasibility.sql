CREATE TABLE task_search_documents (
    document_id INTEGER PRIMARY KEY,
    task_id TEXT NOT NULL,
    source_kind TEXT NOT NULL,
    source_text TEXT NOT NULL
);

CREATE TABLE task_search_fts (
    source_text TEXT NOT NULL,
    rank REAL NOT NULL
);
