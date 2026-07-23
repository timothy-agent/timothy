-- Mission worker turns run through loop.Agent same as chat, but tool-
-- call bookkeeping (session_events, tools audit) hard-requires a real
-- session_id uuid FK -- a mission has no chat session of its own. Give
-- every mission a hidden session row purely so that bookkeeping has
-- something real to attach to; nothing about it is chat-facing (no
-- title shown, sessions list can filter it out by join).
ALTER TABLE missions ADD COLUMN IF NOT EXISTS session_id uuid REFERENCES sessions(id);
