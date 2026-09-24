# Pending live-DB alters

Additive schema changes not yet applied to any live database. Safe to
run before deploy; each entry stays here until confirmed applied on
every live instance, then it's removed.

Issue #826: GitHub event poller cursors. Additive, safe any time
before the new binary boots.

```sql
CREATE TABLE IF NOT EXISTS github_poll_cursors (
    connector_id  uuid NOT NULL REFERENCES connectors(id) ON DELETE CASCADE,
    repo          text NOT NULL,
    etag          text NOT NULL DEFAULT '',
    last_event_id text NOT NULL DEFAULT '',
    runs_since    timestamptz NOT NULL DEFAULT now(),
    next_poll_at  timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (connector_id, repo)
);
```

Issue #857: automation triggers restrict a run's tools. Adds the
per-mission allowlist column (NULL = unrestricted). Run before the new
binary boots; it reads the column on every mission load.

```sql
ALTER TABLE missions ADD COLUMN IF NOT EXISTS tool_allowlist text[];
```

Issue #827: operator notifications without a mission (webhook trigger
guard, automation breaker with no mission). Run before the new binary
boots; it inserts rows with a NULL mission_id.

```sql
ALTER TABLE notifications ALTER COLUMN mission_id DROP NOT NULL;
```

Issue #828: channels (Telegram) with pairing and conversation to
session mapping. Additive, run before the new binary boots; it reads
sessions.origin_kind and the channel tables at startup.

```sql
CREATE TABLE IF NOT EXISTS channels (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name           text NOT NULL,
    kind           text NOT NULL CHECK (kind IN ('telegram','slack','email')),
    config         jsonb NOT NULL DEFAULT '{}',
    credential_ref text,
    agent_id       uuid REFERENCES agents(id) ON DELETE SET NULL,
    state          jsonb NOT NULL DEFAULT '{}',
    enabled        boolean NOT NULL DEFAULT true,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS channels_name_ci ON channels (lower(btrim(name)));
CREATE TABLE IF NOT EXISTS channel_pairings (
    channel_id       uuid NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    external_user_id text NOT NULL,
    display_name     text NOT NULL DEFAULT '',
    status           text NOT NULL CHECK (status IN ('pending','approved','revoked')),
    code             text,
    code_expires_at  timestamptz,
    last_prompt_at   timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (channel_id, external_user_id)
);
CREATE TABLE IF NOT EXISTS channel_conversations (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id         uuid NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    external_chat_id   text NOT NULL,
    external_thread_id text NOT NULL DEFAULT '',
    external_user_id   text NOT NULL,
    session_id         uuid NOT NULL REFERENCES sessions(id),
    agent_id           uuid REFERENCES agents(id) ON DELETE SET NULL,
    last_message_at    timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now(),
    UNIQUE (channel_id, external_chat_id, external_thread_id)
);
CREATE TABLE IF NOT EXISTS channel_inbound (
    channel_id          uuid NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    external_message_id text NOT NULL,
    received_at         timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (channel_id, external_message_id)
);
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS origin_kind text NOT NULL DEFAULT 'web' CHECK (origin_kind IN ('web','api','mission','channel'));
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS channel_conversation_id uuid;
```

Issue #829: channel approvals, mission parks and reply to origin.
Additive, run before the new binary boots; it reads
missions.channel_conversation_id on every mission load. The last
statement lists create_mission on the seeded general agent next to
followup_mission.

```sql
ALTER TABLE missions ADD COLUMN IF NOT EXISTS channel_conversation_id uuid;
ALTER TABLE channel_conversations ADD COLUMN IF NOT EXISTS state jsonb NOT NULL DEFAULT '{}';
UPDATE agents SET tools = tools || '["create_mission"]'::jsonb
WHERE name = 'general' AND tools ? 'followup_mission' AND NOT tools ? 'create_mission';
```

Issue #831: channel trigger and channel destination; the telegram
destination kind is retired. Run in one transaction before the new
binary boots: it no longer delivers kind telegram and rejects it on
create. Every telegram destination becomes a disabled telegram channel
(its name plus " (bot)", a number added on a clash, credential_ref
copied) and a channel destination on it; ids stay, so mission and
automation references keep working. The channels are created disabled
so nothing starts polling a bot token polled elsewhere; enable one in
Settings > Channels only when this Timothy should receive on that bot.
The constraint name is destinations_kind_check on the local database;
confirm it on homelab first with `\d destinations` and adjust the two
ALTER statements if it differs.

```sql
BEGIN;
ALTER TABLE destinations DROP CONSTRAINT destinations_kind_check;
DO $$
DECLARE
    d        record;
    base     text;
    name_try text;
    n        int;
    chan_id  uuid;
    moved    int := 0;
BEGIN
    FOR d IN SELECT id, name, credential_ref, config FROM destinations WHERE kind = 'telegram' ORDER BY created_at, id LOOP
        base := left(btrim(d.name), 54);
        name_try := base || ' (bot)';
        n := 1;
        WHILE EXISTS (SELECT 1 FROM channels WHERE lower(btrim(name)) = lower(btrim(name_try))) LOOP
            n := n + 1;
            name_try := base || ' (bot ' || n || ')';
        END LOOP;
        INSERT INTO channels (name, kind, credential_ref, enabled)
        VALUES (name_try, 'telegram', d.credential_ref, false)
        RETURNING id INTO chan_id;
        UPDATE destinations
        SET kind = 'channel',
            config = jsonb_build_object('channel_id', chan_id::text, 'chat_id', d.config->>'chat_id'),
            credential_ref = '',
            updated_at = now()
        WHERE id = d.id;
        moved := moved + 1;
    END LOOP;
    RAISE NOTICE 'issue 831: % telegram destinations moved to channels', moved;
END $$;
ALTER TABLE destinations ADD CONSTRAINT destinations_kind_check
    CHECK (kind IN ('email', 'webhook', 'channel', 'github', 'bitbucket'));
COMMIT;
```
